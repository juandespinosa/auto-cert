# auto-certs — despliegue en AWS Lambda

Esta guía despliega `cmd/lambda` como una función Lambda corriendo en un
schedule de EventBridge. El binario es el mismo pipeline que `cmd/monitor`,
con tres diferencias inyectadas vía config:

| Recurso | Local | Lambda |
|---|---|---|
| Secrets | `.env` (godotenv) | SSM Parameter Store |
| State | `state/alerts.json` | `s3://bucket/auto-certs/<env>/state/alerts.json` |
| Inventory | `state/inventory.json` (overwrite) | `s3://bucket/auto-certs/<env>/inventory/YYYY-MM-DD.json` |
| Notifier | `dryrun` o `smtp` | `ses` |

---

## Pre-requisitos

1. **AWS CLI** configurado con credenciales (`aws sts get-caller-identity` debe funcionar).
2. **AWS SAM CLI** (`sam --version` ≥ 1.100). Instalación: <https://docs.aws.amazon.com/serverless-application-model/latest/developerguide/install-sam-cli.html>.
3. **Go 1.26+** local — SAM lo invoca vía el `Makefile` en la raíz.
4. **Una región AWS** definida en `~/.aws/config` (o `AWS_REGION` env).
5. **SES identity verificado** para la dirección del remitente (`alerts@bodytechcorp.com` o similar). Verificación:
   ```bash
   aws ses verify-domain-identity --domain bodytechcorp.com
   # ... agregar el TXT en DNS, esperar verificación
   ```

   Para sandbox SES (cuenta nueva) también hay que verificar el destinatario.
   Para producción, pedir "production access" en la consola SES.

---

## Deploy

```bash
cd infra/
sam build --template template.yaml
sam deploy --guided
```

El `--guided` te pregunta:
- **Stack Name**: `auto-certs`
- **Region**: la que prefieras (ej. `us-east-1`)
- **Environment**: `prod` (o `dev`)
- **ScheduleExpression**: `cron(0 14 * * ? *)` (diario 9am Bogotá) o `cron(0 14 1 * ? *)` (mensual día 1)
- **SesFromAddress**: dirección verificada en SES
- **SesToAddress**: destinatario
- **Confirm changes / IAM**: yes

Guarda las respuestas en `samconfig.toml` para próximos deploys (solo
`sam deploy` sin `--guided`).

---

## Post-deploy: cargar secrets reales

El template crea parámetros SSM con valor `PLACEHOLDER` (CloudFormation no
puede crear `SecureString` vacíos). Reemplazá con los reales y rotalos a
`SecureString`:

```bash
ENV=prod
aws ssm put-parameter --overwrite \
  --name /auto-certs/$ENV/NAMECOM_USERNAME \
  --type String --value '<usuario>'

aws ssm put-parameter --overwrite \
  --name /auto-certs/$ENV/NAMECOM_TOKEN \
  --type SecureString --value '<token>'

aws ssm put-parameter --overwrite \
  --name /auto-certs/$ENV/NAMECOM_BASE_URL \
  --type String --value 'https://api.name.com'
```

Si más adelante agregás Cloudflare, SMTP secundario, etc., creá nuevos
parámetros con el mismo prefijo `/auto-certs/$ENV/` — la Lambda los carga
automáticamente al startup.

---

## Probar invocación manual

```bash
aws lambda invoke --function-name auto-certs-prod /tmp/out.json
cat /tmp/out.json   # body de respuesta (null si OK)
sam logs --stack-name auto-certs --tail   # logs JSON en vivo
```

Para ver el inventory recién escrito:
```bash
BUCKET=$(aws cloudformation describe-stacks --stack-name auto-certs \
  --query 'Stacks[0].Outputs[?OutputKey==`StateBucketName`].OutputValue' --output text)

aws s3 ls s3://$BUCKET/auto-certs/prod/inventory/
aws s3 cp s3://$BUCKET/auto-certs/prod/inventory/2026-05-19.json - | jq .
```

---

## Rollback / actualización

Cambio de código → `cd infra/ && sam build && sam deploy`.
Cambio de schedule sin redesplegar binario → editar parámetro en consola o:
```bash
aws events put-rule --name <rule-name> --schedule-expression 'cron(...)'
```

Tear-down completo:
```bash
cd infra/
sam delete --stack-name auto-certs
```

⚠️ El bucket S3 tiene `DeletionPolicy: Retain` — sobrevive al `sam delete`.
Borralo manualmente si querés liberar el nombre.

---

## Costos esperados

Con 30 runs/mes (diario), ~150 dominios, 30s por run:

| Recurso | Mensual |
|---|---|
| Lambda (30 × 30s × 512MB ARM64) | ~$0.01 |
| S3 (state + inventory) | ~$0.001 |
| SES (30 emails) | ~$0.003 |
| CloudWatch Logs (5MB) | ~$0.003 |
| SSM Standard params | $0 (free tier) |
| **Total** | **<$0.02/mes** |

---

## Troubleshooting

**`SAM build` falla con "go: command not found"**
→ Verificá Go local con `go version`. SAM ejecuta el Makefile en tu shell.

**Lambda timeout**
→ Subí `Timeout` en `template.yaml`. 5min cubre ~500 dominios; si tenés más,
considerá batching o subir memoria (256MB→1024MB también acelera).

**SSM permission denied**
→ El IAM role permite solo `parameter/auto-certs/$ENV/*`. Si moviste params
a otro prefijo, ampliá la policy en `template.yaml`.

**SES "Email address not verified"**
→ Estás en sandbox SES: tenés que verificar TANTO from como to. Pedí
production access en la consola.

**Inventory no se actualiza**
→ Revisá CloudWatch Logs por errores de `s3:PutObject`. Lo más probable es
permisos: el role permite operar solo en el bucket creado por el stack.
