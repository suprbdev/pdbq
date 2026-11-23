-- field: allTiles
SELECT json_build_object('nodes', coalesce(json_agg(__s_4.ndata ORDER BY __s_4.__rn ASC) FILTER (WHERE __s_4.__rn <= $8), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'rastWidth', ST_Width(__t_1."rast"), 'rastHeight', ST_Height(__t_1."rast"), 'rastNumBands', ST_NumBands(__t_1."rast"), 'rastSrid', ST_SRID(__t_1."rast"), 'rastPixelWidth', ST_PixelWidth(__t_1."rast"), 'rastPixelHeight', ST_PixelHeight(__t_1."rast"), 'rastEnvelope', ST_AsGeoJSON(ST_Envelope(__t_1."rast"))::json, 'rastValue', (SELECT ST_Value(__rv.r, $2::int4, CASE WHEN ST_SRID(__rv.p) = ST_SRID(__rv.r) OR ST_SRID(__rv.p) = 0 OR ST_SRID(__rv.r) = 0 THEN ST_SetSRID(__rv.p, ST_SRID(__rv.r)) ELSE ST_Transform(__rv.p, ST_SRID(__rv.r)) END, true) FROM (SELECT __t_1."rast" AS r, CASE WHEN left($1, 1) = '{' THEN ST_GeomFromGeoJSON($1) ELSE ST_GeomFromEWKT($1) END AS p) AS __rv), 'b2', (SELECT ST_Value(__rv.r, $4::int4, CASE WHEN ST_SRID(__rv.p) = ST_SRID(__rv.r) OR ST_SRID(__rv.p) = 0 OR ST_SRID(__rv.r) = 0 THEN ST_SetSRID(__rv.p, ST_SRID(__rv.r)) ELSE ST_Transform(__rv.p, ST_SRID(__rv.r)) END, true) FROM (SELECT __t_1."rast" AS r, CASE WHEN left($3, 1) = '{' THEN ST_GeomFromGeoJSON($3) ELSE ST_GeomFromEWKT($3) END AS p) AS __rv), 'rastSummaryStats', (SELECT json_build_object('count', __rs_2.j->'count', 'mean', __rs_2.j->'mean', 'max', __rs_2.j->'max') FROM (SELECT to_json(ST_SummaryStats(__t_1."rast", $5::int4, true)) AS j) AS __rs_2 WHERE __rs_2.j IS NOT NULL), 's2', (SELECT json_build_object('__typename', 'RasterSummaryStats'::text, 'total', __rs_3.j->'sum', 'stddev', __rs_3.j->'stddev', 'min', __rs_3.j->'min') FROM (SELECT to_json(ST_SummaryStats(__t_1."rast", $6::int4, true)) AS j) AS __rs_3 WHERE __rs_3.j IS NOT NULL), 'maskSrid', ST_SRID(__t_1."mask")) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."tiles" AS __t_1
  WHERE ST_Intersects(__t_1."rast", CASE WHEN left($7, 1) = '{' THEN ST_GeomFromGeoJSON($7) ELSE ST_GeomFromEWKT($7) END) AND __t_1."rast" IS NOT NULL
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_4
GROUP BY ()
-- args: ["{\"coordinates\":[1,2],\"type\":\"Point\"}",1,"POINT(1 2)",2,1,2,"SRID=3857;POINT(1 2)",100]

-- field: allTiles
-- error: compile: rastValue: band must be a positive integer

-- field: allTiles
-- error: compile: spatial value "nope" is not valid WKT

