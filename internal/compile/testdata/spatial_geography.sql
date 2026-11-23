-- field: allPlaces
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $8), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  WHERE __t_1."area" && (ST_MakeEnvelope($1, $2, $3, $4, 4326))::geography AND ST_DWithin(__t_1."area", (CASE WHEN left($5, 1) = '{' THEN ST_GeomFromGeoJSON($5) ELSE ST_GeomFromEWKT($5) END)::geography, $6) AND ST_Intersects(__t_1."area", (CASE WHEN left($7, 1) = '{' THEN ST_GeomFromGeoJSON($7) ELSE ST_GeomFromEWKT($7) END)::geography)
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: [-0.2,51.4,0,51.6,"{\"coordinates\":[-0.128,51.508],\"type\":\"Point\"}",500,"SRID=4326;POINT(-0.128 51.508)",100]

-- field: allPlaces
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $6), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  WHERE __t_1."area"::geometry ~ ST_MakeEnvelope($1, $2, $3, $4, 4326) AND ST_Within(__t_1."area"::geometry, CASE WHEN left($5, 1) = '{' THEN ST_GeomFromGeoJSON($5) ELSE ST_GeomFromEWKT($5) END)
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: [-0.2,51.4,0,51.6,"SRID=4326;POLYGON((-1 51,1 51,1 52,-1 51))",100]

