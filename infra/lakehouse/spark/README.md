# Spark tier — silver-layer cleaning jobs (SPEC §13)

Runs from `infra/docker-compose.lakehouse.yml`: `spark-master` (spark://spark-master:7077,
UI on **8081**) + one `spark-worker`. Job sources are mounted read-only into both
containers at `/opt/spark-jobs`.

## Jobs

| Job | Source (Iceberg) | Sink (Iceberg) | What it does |
|---|---|---|---|
| `jobs/silver_clean_bookings.py` | `bronze.booking_events` | `silver.booking_events` | Dedupe on `event_id` (latest `occurred_at` wins), drop unkeyed/timeless rows. |
| `jobs/silver_clean_transcripts.py` | `bronze.transcripts` | `silver.transcripts` | Normalize `role`, dedupe on `(conversation_id, ts, role)`. |

Both jobs create the silver table on first run (schema CTAS-cloned from bronze,
**unpartitioned = spec v1**) and then perform **partition evolution** with
`ALTER TABLE ... ADD PARTITION FIELD days(<ts>)` (**spec v2**). Iceberg keeps old and new
specs readable; re-runs use dynamic partition overwrite, so jobs are idempotent.

## Dependencies (Ivy packages, downloaded once into the `spark-ivy` volume)

- `org.apache.iceberg:iceberg-spark-runtime-3.5_2.12:1.6.1` — Iceberg Spark integration
  (any `1.5.x`/`1.6.x` line matching Spark 3.5 works; pin consistently with the REST catalog).
- `org.apache.iceberg:iceberg-aws-bundle:1.6.1` — shaded AWS SDK v2 for `S3FileIO` (MinIO).
- Alternative to the bundle: `org.apache.hadoop:hadoop-aws:3.3.4` +
  `com.amazonaws:aws-java-sdk-bundle:1.12.262` with `io-impl` switched to
  `org.apache.iceberg.hadoop.HadoopFileIO`. The bundle path above is what the jobs
  configure and is the recommended one.

## Running

```bash
docker compose -f infra/docker-compose.lakehouse.yml up -d   # whole lakehouse tier

docker exec opendesk-spark-master /opt/bitnami/spark/bin/spark-submit \
  --master spark://spark-master:7077 \
  --packages org.apache.iceberg:iceberg-spark-runtime-3.5_2.12:1.6.1,org.apache.iceberg:iceberg-aws-bundle:1.6.1 \
  /opt/spark-jobs/silver_clean_bookings.py

docker exec opendesk-spark-master /opt/bitnami/spark/bin/spark-submit \
  --master spark://spark-master:7077 \
  --packages org.apache.iceberg:iceberg-spark-runtime-3.5_2.12:1.6.1,org.apache.iceberg:iceberg-aws-bundle:1.6.1 \
  /opt/spark-jobs/silver_clean_transcripts.py
```

Environment overrides honoured by the jobs: `ICEBERG_REST_URI` (default
`http://iceberg-rest:8181`), `S3_ENDPOINT` (`http://minio:9000`),
`AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` (`minioadmin`/`minioadmin`).
`revenue_intelligence.py` additionally honours `RECO_WINDOW_DAYS` (default `30`).

Verify in Trino afterwards:

```sql
SELECT * FROM iceberg.silver.booking_events LIMIT 10;
SELECT partition FROM iceberg.silver."booking_events$partitions";
```
