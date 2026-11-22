-- field: allPlaces
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $15), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  WHERE __t_1."location" |>> ST_GeomFromEWKT($1) AND __t_1."location" <<| ST_GeomFromEWKT($2) AND __t_1."location" ~= ST_SetSRID(ST_GeomFromGeoJSON($3), 4326) AND __t_1."location" &&& ST_GeomFromEWKT($4) AND __t_1."location" << ST_GeomFromEWKT($5) AND __t_1."location" |&> ST_GeomFromEWKT($6) AND __t_1."location" &<| ST_GeomFromEWKT($7) AND __t_1."location" &< ST_GeomFromEWKT($8) AND __t_1."location" &> ST_GeomFromEWKT($9) AND __t_1."location" >> ST_GeomFromEWKT($10) AND __t_1."location" = ST_SetSRID(ST_GeomFromGeoJSON($11), 4326) AND ST_3DIntersects(__t_1."location", ST_GeomFromEWKT($12)) AND ST_OrderingEquals(__t_1."location", ST_SetSRID(ST_GeomFromGeoJSON($13), 4326))
  ORDER BY __t_1."id" ASC
  LIMIT $14
) AS __s_2
GROUP BY ()
-- args: ["SRID=4326;POINT(0 50)","SRID=4326;POINT(0 52)","{\"coordinates\":[-0.1281,51.508],\"type\":\"Point\"}","SRID=4326;POLYGON((-1 51,1 51,1 52,-1 51))","SRID=4326;POINT(1 51)","SRID=4326;POINT(0 50)","SRID=4326;POINT(0 52)","SRID=4326;POINT(1 51)","SRID=4326;POINT(-1 51)","SRID=4326;POINT(-1 51)","{\"coordinates\":[-0.1281,51.508],\"type\":\"Point\"}","SRID=4326;POLYGON((-1 51,1 51,1 52,-1 51))","{\"coordinates\":[-0.1281,51.508],\"type\":\"Point\"}",101,100]

-- field: allPlaces
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $8), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  WHERE __t_1."area"::geometry ~= ST_GeomFromEWKT($1) AND __t_1."area"::geometry &&& ST_GeomFromEWKT($2) AND __t_1."area"::geometry << ST_GeomFromEWKT($3) AND __t_1."area" = (ST_GeomFromEWKT($4))::geography AND ST_3DIntersects(__t_1."area"::geometry, ST_GeomFromEWKT($5)) AND ST_OrderingEquals(__t_1."area"::geometry, ST_GeomFromEWKT($6))
  ORDER BY __t_1."id" ASC
  LIMIT $7
) AS __s_2
GROUP BY ()
-- args: ["SRID=4326;POINT(0 51)","SRID=4326;POINT(0 51)","SRID=4326;POINT(1 51)","SRID=4326;POINT(0 51)","SRID=4326;POINT(0 51)","SRID=4326;POINT(0 51)",101,100]

