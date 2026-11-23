-- field: allPlaces
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'name', __t_1."name") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  WHERE ST_DWithin(__t_1."location", CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), 4326) ELSE ST_GeomFromEWKT($1) END, $2)
  ORDER BY __t_1."id" ASC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["{\"coordinates\":[-0.12,51.5],\"type\":\"Point\"}",0.05,101,100]

-- field: allPlaces
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  WHERE ST_DWithin(__t_1."location"::geography, (CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), 4326) ELSE ST_GeomFromEWKT($1) END)::geography, $2)
  ORDER BY __t_1."id" ASC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["SRID=4326;POINT(-0.12 51.5)",1000,101,100]

-- field: allPlaces
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  WHERE NOT ST_DWithin(__t_1."area", (CASE WHEN left($1, 1) = '{' THEN ST_GeomFromGeoJSON($1) ELSE ST_GeomFromEWKT($1) END)::geography, $2)
  ORDER BY __t_1."id" ASC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["{\"coordinates\":[-0.12,51.5],\"type\":\"Point\"}",5000,101,100]

