"""silver_clean_transcripts — bronze → silver for conversation transcripts (SPEC §13).

Reads iceberg.bronze.transcripts (raw ConversationTurn events from
`opendesk.conversation.transcripts`, already PII-redacted by the Fluvio smart module),
deduplicates on (conversation_id, ts, role) keeping one row, normalizes the role to
lowercase, and writes iceberg.silver.transcripts.

Partition evolution mirrors silver_clean_bookings: table created unpartitioned, then
evolved with `days(ts)`; re-runs dynamically overwrite only touched day partitions.

Run (see infra/lakehouse/spark/README.md):

  docker exec opendesk-spark-master /opt/bitnami/spark/bin/spark-submit \
    --master spark://spark-master:7077 \
    --packages org.apache.iceberg:iceberg-spark-runtime-3.5_2.12:1.6.1,org.apache.iceberg:iceberg-aws-bundle:1.6.1 \
    /opt/spark-jobs/silver_clean_transcripts.py
"""

import os

from pyspark.sql import SparkSession, Window
from pyspark.sql import functions as F

SOURCE_TABLE = "iceberg.bronze.transcripts"
TARGET_TABLE = "iceberg.silver.transcripts"
TARGET_NAMESPACE = "iceberg.silver"
PARTITION_SOURCE_COL = "ts"
PARTITION_FIELD = "ts_day"


def build_spark() -> SparkSession:
    """Spark session wired to the Iceberg REST catalog + MinIO (S3FileIO)."""
    return (
        SparkSession.builder.appName("opendesk-silver-clean-transcripts")
        .config("spark.sql.catalog.iceberg", "org.apache.iceberg.spark.SparkCatalog")
        .config("spark.sql.catalog.iceberg.type", "rest")
        .config(
            "spark.sql.catalog.iceberg.uri",
            os.getenv("ICEBERG_REST_URI", "http://iceberg-rest:8181"),
        )
        .config("spark.sql.catalog.iceberg.warehouse", "s3://lake/warehouse")
        .config("spark.sql.catalog.iceberg.io-impl", "org.apache.iceberg.aws.s3.S3FileIO")
        .config(
            "spark.sql.catalog.iceberg.s3.endpoint",
            os.getenv("S3_ENDPOINT", "http://minio:9000"),
        )
        .config("spark.sql.catalog.iceberg.s3.path-style-access", "true")
        .config(
            "spark.sql.catalog.iceberg.s3.access-key-id",
            os.getenv("AWS_ACCESS_KEY_ID", "minioadmin"),
        )
        .config(
            "spark.sql.catalog.iceberg.s3.secret-access-key",
            os.getenv("AWS_SECRET_ACCESS_KEY", "minioadmin"),
        )
        .config("spark.sql.iceberg.handle-timestamp-without-timezone", "true")
        .getOrCreate()
    )


def ensure_target_table(spark: SparkSession) -> None:
    spark.sql(f"CREATE NAMESPACE IF NOT EXISTS {TARGET_NAMESPACE}")

    existing = {r.tableName for r in spark.sql(f"SHOW TABLES IN {TARGET_NAMESPACE}").collect()}
    if "transcripts" not in existing:
        spark.sql(
            f"CREATE TABLE {TARGET_TABLE} USING iceberg AS "
            f"SELECT * FROM {SOURCE_TABLE} WHERE 1 = 0"
        )
        print(f"[silver] created {TARGET_TABLE} (unpartitioned, spec v1)")

    described = [r.col_name for r in spark.sql(f"DESCRIBE {TARGET_TABLE}").collect()]
    if PARTITION_FIELD not in described:
        spark.sql(f"ALTER TABLE {TARGET_TABLE} ADD PARTITION FIELD days({PARTITION_SOURCE_COL})")
        print(f"[silver] evolved partition spec: + days({PARTITION_SOURCE_COL})")
    else:
        print(f"[silver] partition field {PARTITION_FIELD} already present, no evolution needed")


def clean(spark: SparkSession):
    bronze = spark.table(SOURCE_TABLE)

    # Dedupe: Kafka consumer may deliver a turn more than once; the natural key of a turn
    # is (conversation_id, ts, role) — text is retained from the first arrival.
    first = Window.partitionBy("conversation_id", "ts", "role").orderBy(F.col("ts").asc_nulls_last())
    deduped = (
        bronze.withColumn("role", F.lower(F.trim(F.col("role"))))
        .withColumn("_rn", F.row_number().over(first))
        .filter(F.col("_rn") == 1)
        .drop("_rn")
        .filter(F.col("tenant_id").isNotNull() & F.col("conversation_id").isNotNull())
        .filter(F.col("ts").isNotNull())
    )
    return deduped


def main() -> None:
    spark = build_spark()
    try:
        ensure_target_table(spark)
        deduped = clean(spark)

        target_cols = [r.col_name for r in spark.sql(f"DESCRIBE {TARGET_TABLE}").collect()
                       if r.col_name and not r.col_name.startswith("#")][: len(deduped.columns)]
        out = deduped.select([c for c in deduped.columns if c in target_cols])

        out.writeTo(TARGET_TABLE).overwritePartitions()
        print(f"[silver] wrote {out.count()} deduplicated rows to {TARGET_TABLE}")
    finally:
        spark.stop()


if __name__ == "__main__":
    main()
