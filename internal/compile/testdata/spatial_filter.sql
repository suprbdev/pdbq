-- field: allPlaces
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $6), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id", 'name', __t_1."name") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  WHERE __t_1."location" && ST_MakeEnvelope($1, $2, $3, $4, 4326)
  ORDER BY __t_1."id" ASC
  LIMIT $5
) AS __s_2
GROUP BY ()
-- args: [-0.2,51.4,0,51.6,101,100]

-- field: allPlaces
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  WHERE ST_Intersects(__t_1."location", CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), 4326) ELSE ST_GeomFromEWKT($1) END) AND ST_Within(__t_1."location", CASE WHEN left($2, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($2), 4326) ELSE ST_GeomFromEWKT($2) END)
  ORDER BY __t_1."id" ASC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["{\"coordinates\":[[[-0.2,51.4],[0,51.4],[0,51.6],[-0.2,51.4]]],\"type\":\"Polygon\"}","SRID=4326;POLYGON((-1 51,1 51,1 52,-1 51))",101,100]

-- field: allPlaces
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  WHERE __t_1."area" IS NULL AND __t_1."location" IS NOT NULL
  ORDER BY __t_1."id" ASC
  LIMIT $1
) AS __s_2
GROUP BY ()
-- args: [101,100]

