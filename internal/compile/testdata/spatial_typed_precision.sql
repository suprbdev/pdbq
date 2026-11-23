-- field: allPlaces
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('location', (SELECT CASE WHEN json_array_length(__geo.j->'coordinates') = 0 THEN NULL ELSE json_build_object('x', __geo.j->'coordinates'->0, 'y', __geo.j->'coordinates'->1, 'geojson', __geo.j) END FROM (SELECT ST_AsGeoJSON(__geo_v.g, $1::int4)::json AS j, ST_SRID(__geo_v.g) AS srid FROM (SELECT __t_1."location" AS g) AS __geo_v) AS __geo WHERE __geo.j IS NOT NULL), 'area', (SELECT CASE WHEN (__geo.j->>'type') IN ('Point', 'LineString', 'Polygon', 'MultiPoint', 'MultiLineString', 'MultiPolygon', 'GeometryCollection') THEN CASE WHEN 'Geography' || (__geo.j->>'type') || __geo.dims IN ('GeographyPoint', 'GeographyPointM', 'GeographyPointZ', 'GeographyPointZM') THEN CASE WHEN json_array_length(__geo.j->'coordinates') = 0 THEN NULL ELSE json_build_object('__typename', 'Geography' || (__geo.j->>'type') || __geo.dims, 'srid', __geo.srid) END ELSE json_build_object('__typename', 'Geography' || (__geo.j->>'type') || __geo.dims, 'srid', __geo.srid) END END FROM (SELECT ST_AsGeoJSON(__geo_v.g, $3::int4)::json AS j, ST_SRID(__geo_v.g) AS srid, CASE ST_Zmflag(__geo_v.g::geometry) WHEN 1 THEN 'M' WHEN 2 THEN 'Z' WHEN 3 THEN 'ZM' ELSE '' END AS dims FROM (SELECT ST_Buffer(__t_1."area", $2::float8) AS g) AS __geo_v) AS __geo WHERE __geo.j IS NOT NULL)) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: [3,100,5,100]

