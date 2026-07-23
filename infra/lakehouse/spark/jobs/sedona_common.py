"""sedona_common — shared Apache Sedona session builder (SPEC-W8 Part B1).

The lakehouse Spark runtime is `bitnami/spark:3.5` (Spark 3.5, Scala 2.12 —
see infra/docker-compose.lakehouse.yml and infra/lakehouse/spark/README.md),
so the matching Sedona artifacts are:

  * org.apache.sedona:sedona-spark-shaded-3.5_2.12:1.7.0
  * org.datasyslab:geotools-wrapper:1.7.0-28.5   (Sedona's GeoTools dependency,
    versioned <sedona-version>-<geotools-version>)

`build_sedona_context()` mirrors the Iceberg REST catalog + MinIO (S3FileIO)
wiring of the existing jobs (silver_clean_bookings.py, revenue_intelligence.py)
and additionally:

  * merges the Sedona/geotools artifacts into `spark.jars.packages` together
    with the Iceberg packages and anything already configured (spark-defaults,
    `SPARK_JARS_PACKAGES`, or an extra_packages argument), so jobs do NOT need
    `--packages` on the spark-submit command line;
  * installs the Kryo serializer + SedonaKryoRegistrator (required for Sedona's
    geometry shuffle serialization);
  * wraps the session in a SedonaContext, which registers every ST_* SQL
    function (including the H3 family: ST_H3CellIDs / ST_H3ToGeom, available
    since Sedona 1.6.0 and therefore present in the pinned 1.7.0).

H3 vs geohash (documented choice, see geo_analytics.py): the pinned Sedona
1.7.0 ships the H3 function family, so geo_analytics uses H3 res-8 cells.
If the Sedona pin ever drops below 1.6.0, swap to ST_GeoHash-based cells —
the fallback is isolated in geo_analytics.assign_cells().
"""

import os

from pyspark.sql import SparkSession
from pyspark import SparkConf

ICEBERG_PACKAGES = [
    "org.apache.iceberg:iceberg-spark-runtime-3.5_2.12:1.6.1",
    "org.apache.iceberg:iceberg-aws-bundle:1.6.1",
]

SEDONA_PACKAGES = [
    "org.apache.sedona:sedona-spark-shaded-3.5_2.12:1.7.0",
    "org.datasyslab:geotools-wrapper:1.7.0-28.5",
]


def _merged_packages(extra_packages: list[str] | None = None) -> str:
    """Iceberg + Sedona packages merged with any pre-existing packages config.

    Sources, in order: spark-defaults/CLI defaults visible to a fresh
    SparkConf, the SPARK_JARS_PACKAGES env override, our pinned sets, and the
    caller's extra_packages. Duplicates are dropped, order preserved.
    """
    merged: list[str] = []

    def _add(candidates) -> None:
        for pkg in candidates:
            pkg = pkg.strip()
            if pkg and pkg not in merged:
                merged.append(pkg)

    _add(SparkConf().get("spark.jars.packages", "").split(","))
    _add(os.getenv("SPARK_JARS_PACKAGES", "").split(","))
    _add(ICEBERG_PACKAGES)
    _add(SEDONA_PACKAGES)
    _add(extra_packages or [])
    return ",".join(merged)


def build_sedona_context(app_name: str = "opendesk-geo-analytics", extra_packages=None):
    """SedonaContext wired to the Iceberg REST catalog + MinIO (S3FileIO).

    Same catalog configuration as the non-spatial jobs so all gold tables land
    in the same `iceberg` catalog and stay Trino-visible.
    """
    # Imported lazily so this module stays importable on driver-less lint/CI
    # boxes that have pyspark but not the sedona Python package.
    from sedona.spark import SedonaContext

    builder = (
        SparkSession.builder.appName(app_name)
        .config("spark.jars.packages", _merged_packages(extra_packages))
        # Sedona geometry serialization for shuffles (ST_Within spatial joins).
        .config("spark.serializer", "org.apache.spark.serializer.KryoSerializer")
        .config("spark.kryo.registrator", "org.apache.sedona.core.serde.SedonaKryoRegistrator")
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
    )
    spark = builder.getOrCreate()
    # Registers all ST_* SQL functions (ST_Point, ST_GeomFromGeoJSON, ST_Within,
    # ST_H3CellIDs, ST_H3ToGeom, ...) on this session.
    return SedonaContext.create(spark)
