"""geo_analytics — gold geospatial tables (SPEC-W8 Part B1).

Reads:

  * iceberg.silver.booking_events          — deduped booking events
    (silver_clean_bookings.py); BookingCreated rows supply demand timing
    (starts_at, falling back to occurred_at).
  * CONTACT LOCATIONS extract (GeoParquet/parquet on MinIO) — per-booking
    WGS84 points. INPUT CONTRACT (TODO producer): one row per booking with the
    booking contact's latest known location, exported from booking-service
    Postgres (`contact_locations` joined to bookings — Part A owns the
    operational tables; an analytics-pipeline/JDBC exporter lands the extract).
    Path: env GEO_CONTACT_LOCATIONS_PATH (default
    s3://lake/extracts/contact_locations/). Columns:
      tenant_id string, booking_id string, contact_id string,
      lat double, lng double, source string, updated_at timestamp
  * SERVICE AREAS extract — tenant polygons from booking-service
    `service_areas` (Part A). INPUT CONTRACT (TODO producer): same exporter,
    geometry serialised with ST_AsGeoJSON. Path: env GEO_SERVICE_AREAS_PATH
    (default s3://lake/extracts/service_areas/); format env
    GEO_SERVICE_AREAS_FORMAT = parquet (default) | geojson.
      parquet columns: tenant_id string, service_area_id string, name string,
                       geojson string (GeoJSON Polygon/MultiPolygon)
      geojson: a FeatureCollection whose features carry properties
               {tenant_id, service_area_id, name}

Both extracts are OPTIONAL at runtime: a missing path logs a warning and the
job writes empty/partition-only gold tables instead of failing the pipeline.

Outputs (Iceberg, Trino-visible, dynamic partition overwrite like the other
gold jobs; geometry stored as WKT because Iceberg has no geometry type):

  * gold.geo_demand_h3            — bookings per H3 res-8 cell per tenant per
                                    day, incl. cell geometry (cell_wkt).
  * gold.geo_service_area_coverage— bookings inside vs outside each service
                                    area per tenant per day (ST_Within).
  * gold.geo_hourly_density       — demand heat cells by hour-of-week for
                                    staffing (no geometry; join back to
                                    geo_demand_h3 on h3_cell or regenerate
                                    with ST_H3ToGeom).

H3 vs geohash: the pinned Sedona (1.7.0, Spark 3.5 — see sedona_common.py)
ships the H3 family (ST_H3CellIDs / ST_H3ToGeom, added in Sedona 1.6.0), so
this job uses H3 res-8 cells. If the pin ever drops below 1.6.0, switch
assign_cells() to ST_GeoHash(geom, 12)-based cells and rebuild cell geometry
via ST_GeomFromGeoHash.

Run (packages are injected by sedona_common; no --packages needed):

  docker exec opendesk-spark-master /opt/bitnami/spark/bin/spark-submit \
    --master spark://spark-master:7077 \
    /opt/spark-jobs/geo_analytics.py
"""

import os

from pyspark.sql import DataFrame, SparkSession
from pyspark.sql import functions as F
from pyspark.sql import types as T

from sedona_common import build_sedona_context

BOOKINGS_TABLE = "iceberg.silver.booking_events"
TARGET_NAMESPACE = "iceberg.gold"
DEMAND_H3_TABLE = "iceberg.gold.geo_demand_h3"
COVERAGE_TABLE = "iceberg.gold.geo_service_area_coverage"
HOURLY_TABLE = "iceberg.gold.geo_hourly_density"

H3_RESOLUTION = int(os.getenv("GEO_H3_RESOLUTION", "8"))
CONTACT_LOCATIONS_PATH = os.getenv(
    "GEO_CONTACT_LOCATIONS_PATH", "s3://lake/extracts/contact_locations/"
)
SERVICE_AREAS_PATH = os.getenv(
    "GEO_SERVICE_AREAS_PATH", "s3://lake/extracts/service_areas/"
)
SERVICE_AREAS_FORMAT = os.getenv("GEO_SERVICE_AREAS_FORMAT", "parquet").lower()

POINTS_SCHEMA = T.StructType(
    [
        T.StructField("tenant_id", T.StringType()),
        T.StructField("booking_id", T.StringType()),
        T.StructField("contact_id", T.StringType()),
        T.StructField("lat", T.DoubleType()),
        T.StructField("lng", T.DoubleType()),
        T.StructField("source", T.StringType()),
        T.StructField("updated_at", T.TimestampType()),
    ]
)
AREAS_SCHEMA = T.StructType(
    [
        T.StructField("tenant_id", T.StringType()),
        T.StructField("service_area_id", T.StringType()),
        T.StructField("name", T.StringType()),
        T.StructField("geojson", T.StringType()),
    ]
)


# ---------------------------------------------------------------------------
# Inputs
# ---------------------------------------------------------------------------

def read_contact_locations(spark: SparkSession) -> DataFrame:
    """Per-booking WGS84 points from the extract, or an empty contract frame."""
    try:
        df = spark.read.parquet(CONTACT_LOCATIONS_PATH)
    except Exception as exc:  # path missing / no files yet — TODO producer above
        print(f"[geo] WARNING: contact-locations extract unreadable at "
              f"{CONTACT_LOCATIONS_PATH} ({exc.__class__.__name__}); using empty input")
        return spark.createDataFrame([], POINTS_SCHEMA)
    return (
        df.filter(F.col("lat").isNotNull() & F.col("lng").isNotNull())
        .filter(F.col("lat").between(-90, 90) & F.col("lng").between(-180, 180))
        .filter(F.col("tenant_id").isNotNull() & F.col("booking_id").isNotNull())
        .dropDuplicates(["tenant_id", "booking_id"])
    )


def read_service_areas(spark: SparkSession) -> DataFrame:
    """Tenant service-area polygons, or an empty contract frame."""
    try:
        if SERVICE_AREAS_FORMAT == "geojson":
            # FeatureCollection file(s). Features are extracted as raw JSON
            # strings (get_json_object returns subtree text), so arbitrary
            # Polygon/MultiPolygon geometry objects survive re-serialisation
            # into ST_GeomFromGeoJSON without a fixed struct schema.
            raw = spark.read.text(SERVICE_AREAS_PATH, wholetext=True)
            df = (
                raw.select(
                    F.from_json(
                        "value",
                        T.StructType(
                            [T.StructField("features", T.ArrayType(T.StringType()))]
                        ),
                    ).alias("fc")
                )
                .select(F.explode("fc.features").alias("f"))
                .select(
                    F.get_json_object("f", "$.properties.tenant_id").alias("tenant_id"),
                    F.get_json_object("f", "$.properties.service_area_id").alias("service_area_id"),
                    F.get_json_object("f", "$.properties.name").alias("name"),
                    F.get_json_object("f", "$.geometry").alias("geojson"),
                )
            )
        else:
            df = spark.read.parquet(SERVICE_AREAS_PATH)
    except Exception as exc:
        print(f"[geo] WARNING: service-areas extract unreadable at "
              f"{SERVICE_AREAS_PATH} ({exc.__class__.__name__}); using empty input")
        return spark.createDataFrame([], AREAS_SCHEMA)
    return df.filter(
        F.col("tenant_id").isNotNull()
        & F.col("service_area_id").isNotNull()
        & F.col("geojson").isNotNull()
    )


def booking_points(spark: SparkSession) -> DataFrame:
    """BookingCreated demand rows with geometry + day/hour-of-week columns."""
    created = (
        spark.table(BOOKINGS_TABLE)
        .filter(F.lower(F.col("event_type")) == "bookingcreated")
        .filter(F.col("tenant_id").isNotNull() & F.col("booking_id").isNotNull())
        .dropDuplicates(["tenant_id", "booking_id"])
        .select(
            "tenant_id",
            "booking_id",
            F.coalesce(F.col("starts_at"), F.col("occurred_at")).alias("demand_at"),
        )
        .filter(F.col("demand_at").isNotNull())
    )
    points = read_contact_locations(spark).select("tenant_id", "booking_id", "lat", "lng")
    return (
        created.join(points, ["tenant_id", "booking_id"], "inner")
        # ST_Point(x, y) = ST_Point(lng, lat) — longitude first.
        .withColumn("geom", F.expr("ST_Point(cast(lng as double), cast(lat as double))"))
        .withColumn("day", F.to_date("demand_at"))
        .withColumn("day_of_week", F.dayofweek("demand_at"))  # 1=Sunday..7=Saturday
        .withColumn("hour", F.hour("demand_at"))
    )


def assign_cells(df: DataFrame) -> DataFrame:
    """Add H3 res-N cell columns. See module docstring for the H3-vs-geohash
    decision; the geohash fallback (if Sedona < 1.6.0 is ever pinned) is:
        .withColumn("geohash", F.expr("ST_GeoHash(geom, 12)"))
    plus ST_GeomFromGeoHash for the cell polygon."""
    return (
        df.withColumn("h3_cell", F.expr(f"ST_H3CellIDs(geom, {H3_RESOLUTION})[0]"))
        # H3 string index = hex of the uint64 cell id (Trino/JS interop).
        .withColumn("h3_cell_str", F.lower(F.hex("h3_cell")))
    )


# ---------------------------------------------------------------------------
# Gold tables
# ---------------------------------------------------------------------------

def ensure_target_tables(spark: SparkSession) -> None:
    spark.sql(f"CREATE NAMESPACE IF NOT EXISTS {TARGET_NAMESPACE}")
    spark.sql(
        f"""
        CREATE TABLE IF NOT EXISTS {DEMAND_H3_TABLE} (
            tenant_id    STRING,
            day          DATE,
            h3_cell      BIGINT,
            h3_cell_str  STRING,
            cell_wkt     STRING,
            bookings     BIGINT
        ) USING iceberg
        PARTITIONED BY (tenant_id)
        """
    )
    spark.sql(
        f"""
        CREATE TABLE IF NOT EXISTS {COVERAGE_TABLE} (
            tenant_id          STRING,
            day                DATE,
            service_area_id    STRING,
            service_area_name  STRING,
            bookings_inside    BIGINT,
            bookings_outside   BIGINT,
            coverage_share     DOUBLE
        ) USING iceberg
        PARTITIONED BY (tenant_id)
        """
    )
    spark.sql(
        f"""
        CREATE TABLE IF NOT EXISTS {HOURLY_TABLE} (
            tenant_id    STRING,
            day_of_week  INT,
            hour         INT,
            h3_cell      BIGINT,
            h3_cell_str  STRING,
            bookings     BIGINT
        ) USING iceberg
        PARTITIONED BY (tenant_id)
        """
    )


def compute_demand_h3(points: DataFrame) -> DataFrame:
    """Bookings per H3 cell per tenant per day, incl. cell polygon as WKT."""
    return (
        assign_cells(points)
        .groupBy("tenant_id", "day", "h3_cell", "h3_cell_str")
        .count()
        .withColumnRenamed("count", "bookings")
        # Cell polygon: ST_H3ToGeom(array(<cell>)) → WKT for Trino/GeoJSON use.
        .withColumn("cell_wkt", F.expr("ST_AsText(ST_H3ToGeom(array(h3_cell)))"))
        .select("tenant_id", "day", "h3_cell", "h3_cell_str", "cell_wkt", "bookings")
    )


def compute_coverage(spark: SparkSession, points: DataFrame) -> DataFrame:
    """Bookings inside vs outside each service area (ST_Within spatial join).

    Sedona ST_Within on EPSG:4326 lon/lat is a planar point-in-polygon
    predicate in degrees — exact for containment semantics (no buffering).
    """
    areas = read_service_areas(spark).withColumn(
        "area_geom", F.expr("ST_GeomFromGeoJSON(geojson)")
    )
    joined = (
        points.alias("p")
        .join(areas.alias("a"), "tenant_id", "inner")
        .withColumn("inside", F.expr("ST_Within(p.geom, a.area_geom)"))
    )
    inside = (
        joined.groupBy("tenant_id", "day", "service_area_id", "name")
        .agg(F.sum(F.when(F.col("inside"), 1).otherwise(0)).alias("bookings_inside"))
        .withColumnRenamed("name", "service_area_name")
    )
    tenant_day_totals = points.groupBy("tenant_id", "day").count().withColumnRenamed(
        "count", "total_bookings"
    )
    return (
        inside.join(tenant_day_totals, ["tenant_id", "day"], "inner")
        .withColumn("bookings_outside", F.col("total_bookings") - F.col("bookings_inside"))
        .withColumn(
            "coverage_share",
            F.col("bookings_inside") / F.nullif(F.col("total_bookings"), F.lit(0)),
        )
        .select(
            "tenant_id",
            "day",
            "service_area_id",
            "service_area_name",
            "bookings_inside",
            "bookings_outside",
            "coverage_share",
        )
    )


def compute_hourly_density(points: DataFrame) -> DataFrame:
    """Demand heat cells by hour-of-week (for staffing heatmaps)."""
    return (
        assign_cells(points)
        .groupBy("tenant_id", "day_of_week", "hour", "h3_cell", "h3_cell_str")
        .count()
        .withColumnRenamed("count", "bookings")
        .select("tenant_id", "day_of_week", "hour", "h3_cell", "h3_cell_str", "bookings")
    )


def main() -> None:
    spark = build_sedona_context()
    try:
        ensure_target_tables(spark)
        points = booking_points(spark).cache()
        print(f"[geo] {points.count()} geolocated BookingCreated demand rows")

        demand = compute_demand_h3(points)
        demand.writeTo(DEMAND_H3_TABLE).overwritePartitions()
        print(f"[gold] wrote {demand.count()} rows to {DEMAND_H3_TABLE}")

        coverage = compute_coverage(spark, points)
        coverage.writeTo(COVERAGE_TABLE).overwritePartitions()
        print(f"[gold] wrote {coverage.count()} rows to {COVERAGE_TABLE}")

        hourly = compute_hourly_density(points)
        hourly.writeTo(HOURLY_TABLE).overwritePartitions()
        print(f"[gold] wrote {hourly.count()} rows to {HOURLY_TABLE}")
    finally:
        spark.stop()


if __name__ == "__main__":
    main()
