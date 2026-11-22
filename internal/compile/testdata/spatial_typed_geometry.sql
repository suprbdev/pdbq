-- field: allPlaces
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $3), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('location', (SELECT jsonb_build_object('__typename', 'GeometryPoint', 'x', __geo.j->'coordinates'->0, 'y', __geo.j->'coordinates'->1, 'srid', __geo.srid, 'geojson', __geo.j) FROM (SELECT ST_AsGeoJSON(__geo_v.g)::jsonb AS j, ST_SRID(__geo_v.g) AS srid FROM (SELECT __t_1."location" AS g) AS __geo_v) AS __geo WHERE __geo.j IS NOT NULL), 'projected', (SELECT jsonb_build_object('x', __geo.j->'coordinates'->0, 'y', __geo.j->'coordinates'->1, 'srid', __geo.srid) FROM (SELECT ST_AsGeoJSON(__geo_v.g)::jsonb AS j, ST_SRID(__geo_v.g) AS srid FROM (SELECT ST_Transform(ST_SimplifyPreserveTopology(__t_1."location", $1), 3857) AS g) AS __geo_v) AS __geo WHERE __geo.j IS NOT NULL)) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT $2
) AS __s_2
GROUP BY ()
-- args: [0.5,101,100]

-- field: allPlaces
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('area', (SELECT jsonb_build_object('__typename', 'Geography' || (__geo.j->>'type') || __geo.dims, 'srid', __geo.srid, 'geojson', __geo.j) FROM (SELECT ST_AsGeoJSON(__geo_v.g)::jsonb AS j, ST_SRID(__geo_v.g) AS srid, CASE ST_Zmflag(__geo_v.g::geometry) WHEN 1 THEN 'M' WHEN 2 THEN 'Z' WHEN 3 THEN 'ZM' ELSE '' END AS dims FROM (SELECT __t_1."area" AS g) AS __geo_v) AS __geo WHERE __geo.j IS NOT NULL)) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT $1
) AS __s_2
GROUP BY ()
-- args: [101,100]

-- field: allPlaces
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('area', (SELECT CASE WHEN 'Geography' || (__geo.j->>'type') || __geo.dims IN ('GeographyPoint') THEN jsonb_build_object('__typename', 'Geography' || (__geo.j->>'type') || __geo.dims, 'longitude', __geo.j->'coordinates'->0, 'latitude', __geo.j->'coordinates'->1) WHEN 'Geography' || (__geo.j->>'type') || __geo.dims IN ('GeographyLineString', 'GeographyMultiPoint') THEN jsonb_build_object('__typename', 'Geography' || (__geo.j->>'type') || __geo.dims, 'points', (SELECT coalesce(jsonb_agg(jsonb_build_object('longitude', __geo_3.v->0, 'latitude', __geo_3.v->1) ORDER BY __geo_3.i), '[]'::jsonb) FROM jsonb_array_elements(__geo.j->'coordinates') WITH ORDINALITY AS __geo_3(v, i))) WHEN 'Geography' || (__geo.j->>'type') || __geo.dims IN ('GeographyPolygon') THEN jsonb_build_object('__typename', 'Geography' || (__geo.j->>'type') || __geo.dims, 'exterior', CASE WHEN __geo.j->'coordinates'->0 IS NULL THEN NULL ELSE jsonb_build_object('points', (SELECT coalesce(jsonb_agg(jsonb_build_object('longitude', __geo_4.v->0, 'latitude', __geo_4.v->1) ORDER BY __geo_4.i), '[]'::jsonb) FROM jsonb_array_elements(__geo.j->'coordinates'->0) WITH ORDINALITY AS __geo_4(v, i))) END, 'interiors', (SELECT coalesce(jsonb_agg(jsonb_build_object('points', (SELECT coalesce(jsonb_agg(jsonb_build_object('longitude', __geo_4.v->0, 'latitude', __geo_4.v->1) ORDER BY __geo_4.i), '[]'::jsonb) FROM jsonb_array_elements(__geo_3.v) WITH ORDINALITY AS __geo_4(v, i))) ORDER BY __geo_3.i), '[]'::jsonb) FROM jsonb_array_elements(__geo.j->'coordinates') WITH ORDINALITY AS __geo_3(v, i) WHERE __geo_3.i > 1)) WHEN 'Geography' || (__geo.j->>'type') || __geo.dims IN ('GeographyGeometryCollection') THEN jsonb_build_object('__typename', 'Geography' || (__geo.j->>'type') || __geo.dims, 'geometries', (SELECT coalesce(jsonb_agg(CASE WHEN 'Geography' || (__geo_3.v->>'type') || __geo.dims IN ('GeographyPoint') THEN jsonb_build_object('__typename', 'Geography' || (__geo_3.v->>'type') || __geo.dims, 'longitude', __geo_3.v->'coordinates'->0) ELSE jsonb_build_object('__typename', 'Geography' || (__geo_3.v->>'type') || __geo.dims) END ORDER BY __geo_3.i), '[]'::jsonb) FROM jsonb_array_elements(__geo.j->'geometries') WITH ORDINALITY AS __geo_3(v, i))) WHEN 'Geography' || (__geo.j->>'type') || __geo.dims IN ('GeographyPointZ') THEN jsonb_build_object('__typename', 'Geography' || (__geo.j->>'type') || __geo.dims, 'longitude', __geo.j->'coordinates'->0, 'latitude', __geo.j->'coordinates'->1, 'height', __geo.j->'coordinates'->2) ELSE jsonb_build_object('__typename', 'Geography' || (__geo.j->>'type') || __geo.dims) END FROM (SELECT ST_AsGeoJSON(__geo_v.g)::jsonb AS j, ST_SRID(__geo_v.g) AS srid, CASE ST_Zmflag(__geo_v.g::geometry) WHEN 1 THEN 'M' WHEN 2 THEN 'Z' WHEN 3 THEN 'ZM' ELSE '' END AS dims FROM (SELECT __t_1."area" AS g) AS __geo_v) AS __geo WHERE __geo.j IS NOT NULL)) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT $1
) AS __s_2
GROUP BY ()
-- args: [101,100]

