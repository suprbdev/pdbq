-- field: placesNear
SELECT coalesce(jsonb_agg(__s_2.data), '[]'::jsonb) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id", 'name', __t_1."name") AS data
  FROM "public"."places_near"("pt" => CASE WHEN left($1, 1) = '{' THEN ST_GeomFromGeoJSON($1) ELSE ST_GeomFromEWKT($1) END::"public"."geometry") AS __t_1
) AS __s_2
-- args: ["{\"coordinates\":[-0.1281,51.508],\"type\":\"Point\"}"]

-- field: placesNear
SELECT coalesce(jsonb_agg(__s_2.data), '[]'::jsonb) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS data
  FROM "public"."places_near"("pt" => CASE WHEN left($1, 1) = '{' THEN ST_GeomFromGeoJSON($1) ELSE ST_GeomFromEWKT($1) END::"public"."geometry", "max_dist" => $2::"pg_catalog"."float8") AS __t_1
) AS __s_2
-- args: ["SRID=4326;POINT(-0.1281 51.508)",50]

