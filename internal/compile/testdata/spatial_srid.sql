-- field: allPlaces
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  WHERE ST_Intersects(__t_1."location", CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), 4326) ELSE ST_GeomFromEWKT($1) END)
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: ["SRID=4326;POINT(1 2)",100]

-- field: allPlaces
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  WHERE ST_Intersects(__t_1."location", CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), 4326) ELSE ST_GeomFromEWKT($1) END)
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: ["SRID=3857;POINT(1 2)",100]

-- field: allPlaces
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  WHERE ST_Intersects(__t_1."location", CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), 4326) ELSE ST_GeomFromEWKT($1) END)
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: ["{\"coordinates\":[111319,222684],\"crs\":{\"properties\":{\"name\":\"EPSG:3857\"},\"type\":\"name\"},\"type\":\"Point\"}",100]

-- field: allPlaces
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $5), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  WHERE __t_1."location" && ST_MakeEnvelope($1, $2, $3, $4, 4326)
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: [0,0,1,1,100]

-- field: allPlaces
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."location" <-> CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), 4326) ELSE ST_GeomFromEWKT($1) END ASC, __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  ORDER BY __t_1."location" <-> CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), 4326) ELSE ST_GeomFromEWKT($1) END ASC, __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: ["SRID=4326;POINT(0 0)",100]

