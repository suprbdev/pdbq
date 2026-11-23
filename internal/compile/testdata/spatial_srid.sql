-- field: allPlaces
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $3), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  WHERE ST_Intersects(__t_1."location", ST_SetSRID(ST_GeomFromEWKT($1), 4326))
  ORDER BY __t_1."id" ASC
  LIMIT $2
) AS __s_2
GROUP BY ()
-- args: ["POINT(1 2)",101,100]

-- field: allPlaces
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $3), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  WHERE ST_Intersects(__t_1."location", ST_GeomFromEWKT($1))
  ORDER BY __t_1."id" ASC
  LIMIT $2
) AS __s_2
GROUP BY ()
-- args: ["SRID=3857;POINT(1 2)",101,100]

-- field: allPlaces
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $3), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  WHERE ST_Intersects(__t_1."location", ST_Transform(ST_SetSRID(ST_GeomFromGeoJSON($1), 3857), 4326))
  ORDER BY __t_1."id" ASC
  LIMIT $2
) AS __s_2
GROUP BY ()
-- args: ["{\"coordinates\":[111319,222684],\"crs\":{\"properties\":{\"name\":\"EPSG:3857\"},\"type\":\"name\"},\"type\":\"Point\"}",101,100]

-- field: allPlaces
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $6), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  WHERE __t_1."location" && ST_MakeEnvelope($1, $2, $3, $4, 4326)
  ORDER BY __t_1."id" ASC
  LIMIT $5
) AS __s_2
GROUP BY ()
-- args: [0,0,1,1,101,100]

-- field: allPlaces
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $3), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."location" <-> ST_SetSRID(ST_GeomFromEWKT($1), 4326) ASC, __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  ORDER BY __t_1."location" <-> ST_SetSRID(ST_GeomFromEWKT($1), 4326) ASC, __t_1."id" ASC
  LIMIT $2
) AS __s_2
GROUP BY ()
-- args: ["POINT(0 0)",101,100]

