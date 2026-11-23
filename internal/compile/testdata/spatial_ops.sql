-- field: allPlaces
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $14), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  WHERE __t_1."location" |>> CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), 4326) ELSE ST_GeomFromEWKT($1) END AND __t_1."location" <<| CASE WHEN left($2, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($2), 4326) ELSE ST_GeomFromEWKT($2) END AND __t_1."location" ~= CASE WHEN left($3, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($3), 4326) ELSE ST_GeomFromEWKT($3) END AND __t_1."location" &&& CASE WHEN left($4, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($4), 4326) ELSE ST_GeomFromEWKT($4) END AND __t_1."location" << CASE WHEN left($5, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($5), 4326) ELSE ST_GeomFromEWKT($5) END AND __t_1."location" |&> CASE WHEN left($6, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($6), 4326) ELSE ST_GeomFromEWKT($6) END AND __t_1."location" &<| CASE WHEN left($7, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($7), 4326) ELSE ST_GeomFromEWKT($7) END AND __t_1."location" &< CASE WHEN left($8, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($8), 4326) ELSE ST_GeomFromEWKT($8) END AND __t_1."location" &> CASE WHEN left($9, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($9), 4326) ELSE ST_GeomFromEWKT($9) END AND __t_1."location" >> CASE WHEN left($10, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($10), 4326) ELSE ST_GeomFromEWKT($10) END AND __t_1."location" = CASE WHEN left($11, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($11), 4326) ELSE ST_GeomFromEWKT($11) END AND ST_3DIntersects(__t_1."location", CASE WHEN left($12, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($12), 4326) ELSE ST_GeomFromEWKT($12) END) AND ST_OrderingEquals(__t_1."location", CASE WHEN left($13, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($13), 4326) ELSE ST_GeomFromEWKT($13) END)
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: ["SRID=4326;POINT(0 50)","SRID=4326;POINT(0 52)","{\"coordinates\":[-0.1281,51.508],\"type\":\"Point\"}","SRID=4326;POLYGON((-1 51,1 51,1 52,-1 51))","SRID=4326;POINT(1 51)","SRID=4326;POINT(0 50)","SRID=4326;POINT(0 52)","SRID=4326;POINT(1 51)","SRID=4326;POINT(-1 51)","SRID=4326;POINT(-1 51)","{\"coordinates\":[-0.1281,51.508],\"type\":\"Point\"}","SRID=4326;POLYGON((-1 51,1 51,1 52,-1 51))","{\"coordinates\":[-0.1281,51.508],\"type\":\"Point\"}",100]

-- field: allPlaces
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $7), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  WHERE __t_1."area"::geometry ~= CASE WHEN left($1, 1) = '{' THEN ST_GeomFromGeoJSON($1) ELSE ST_GeomFromEWKT($1) END AND __t_1."area"::geometry &&& CASE WHEN left($2, 1) = '{' THEN ST_GeomFromGeoJSON($2) ELSE ST_GeomFromEWKT($2) END AND __t_1."area"::geometry << CASE WHEN left($3, 1) = '{' THEN ST_GeomFromGeoJSON($3) ELSE ST_GeomFromEWKT($3) END AND __t_1."area" = (CASE WHEN left($4, 1) = '{' THEN ST_GeomFromGeoJSON($4) ELSE ST_GeomFromEWKT($4) END)::geography AND ST_3DIntersects(__t_1."area"::geometry, CASE WHEN left($5, 1) = '{' THEN ST_GeomFromGeoJSON($5) ELSE ST_GeomFromEWKT($5) END) AND ST_OrderingEquals(__t_1."area"::geometry, CASE WHEN left($6, 1) = '{' THEN ST_GeomFromGeoJSON($6) ELSE ST_GeomFromEWKT($6) END)
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: ["SRID=4326;POINT(0 51)","SRID=4326;POINT(0 51)","SRID=4326;POINT(1 51)","SRID=4326;POINT(0 51)","SRID=4326;POINT(0 51)","SRID=4326;POINT(0 51)",100]

