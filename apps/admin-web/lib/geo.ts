/**
 * Geospatial helpers shared by the Locations and Geo campaigns dashboards
 * (SPEC-W8 Part C). Pure data/types only — nothing here touches WebGL, so it
 * is safe to import from server or client components.
 */
import type { Feature, Geometry, MultiPolygon, Polygon } from "geojson";
import type { StyleSpecification } from "maplibre-gl";
import type { ServiceArea } from "@/lib/types";

/**
 * OpenStreetMap raster basemap as an inline style — no token or external
 * style URL required. `glyphs` is included so symbol layers (cluster counts)
 * can render text; the tiles themselves come from the public OSM raster
 * endpoint and carry the required attribution. Saturation is dialled down to
 * match the dashboard's warm, low-saturation look.
 */
export const OSM_RASTER_STYLE: StyleSpecification = {
  version: 8,
  sources: {
    osm: {
      type: "raster",
      tiles: ["https://tile.openstreetmap.org/{z}/{x}/{y}.png"],
      tileSize: 256,
      attribution: "© OpenStreetMap contributors",
    },
  },
  glyphs: "https://demotiles.maplibre.org/font/{fontstack}/{range}.pbf",
  layers: [
    {
      id: "osm",
      type: "raster",
      source: "osm",
      paint: { "raster-saturation": -0.3 },
    },
  ],
};

/** Default world view before any data is fitted. */
export const DEFAULT_CENTER: [number, number] = [3.3792, 6.5244]; // Lagos
export const DEFAULT_ZOOM = 2;

const EARTH_RADIUS_M = 6371000;

/**
 * Approximate a geodesic circle as a 64-point GeoJSON polygon — the shape the
 * audience preview and campaign APIs accept for radius targeting.
 */
export function circlePolygon(
  center: { lat: number; lng: number },
  radiusM: number,
  points = 64,
): Feature<Polygon> {
  const ring: [number, number][] = [];
  const latRad = (center.lat * Math.PI) / 180;
  const lonRad = (center.lng * Math.PI) / 180;
  const angular = radiusM / EARTH_RADIUS_M;
  for (let i = 0; i <= points; i++) {
    const bearing = (i / points) * 2 * Math.PI;
    const lat2 = Math.asin(
      Math.sin(latRad) * Math.cos(angular) +
        Math.cos(latRad) * Math.sin(angular) * Math.cos(bearing),
    );
    const lon2 =
      lonRad +
      Math.atan2(
        Math.sin(bearing) * Math.sin(angular) * Math.cos(latRad),
        Math.cos(angular) - Math.sin(latRad) * Math.sin(lat2),
      );
    ring.push([(lon2 * 180) / Math.PI, (lat2 * 180) / Math.PI]);
  }
  return {
    type: "Feature",
    properties: {},
    geometry: { type: "Polygon", coordinates: [ring] },
  };
}

/** "500 m" / "2.5 km" label for the radius slider. */
export function formatRadius(radiusM: number): string {
  if (radiusM >= 1000) {
    const km = radiusM / 1000;
    return `${Number.isInteger(km) ? km : km.toFixed(1)} km`;
  }
  return `${radiusM} m`;
}

/** Bounds [[minLng, minLat], [maxLng, maxLat]] over a set of lng/lat pairs. */
export function boundsOf(
  coords: { lat: number; lng: number }[],
): [[number, number], [number, number]] | null {
  if (coords.length === 0) return null;
  let minLng = Infinity;
  let minLat = Infinity;
  let maxLng = -Infinity;
  let maxLat = -Infinity;
  for (const c of coords) {
    if (c.lng < minLng) minLng = c.lng;
    if (c.lng > maxLng) maxLng = c.lng;
    if (c.lat < minLat) minLat = c.lat;
    if (c.lat > maxLat) maxLat = c.lat;
  }
  return [
    [minLng, minLat],
    [maxLng, maxLat],
  ];
}

/**
 * Normalise a service-area row into a GeoJSON feature. The booking-service
 * may return the geometry under `geojson` or `geom`, as an object or a JSON
 * string, and as either a bare geometry or a full feature — accept them all.
 * Returns null when no usable polygon geometry is present.
 */
export function serviceAreaToFeature(
  area: ServiceArea,
): Feature<Polygon | MultiPolygon> | null {
  const raw = area.geojson ?? area.geom;
  if (!raw) return null;
  let parsed: unknown = raw;
  if (typeof raw === "string") {
    try {
      parsed = JSON.parse(raw);
    } catch {
      return null;
    }
  }
  if (typeof parsed !== "object" || parsed === null) return null;
  const candidate = parsed as {
    type?: string;
    geometry?: Geometry;
  };
  const geometry =
    candidate.type === "Feature" && candidate.geometry
      ? candidate.geometry
      : (candidate as unknown as Geometry);
  if (geometry.type !== "Polygon" && geometry.type !== "MultiPolygon") {
    return null;
  }
  return {
    type: "Feature",
    properties: { areaId: area.id, name: area.name },
    geometry: geometry as Polygon | MultiPolygon,
  };
}
