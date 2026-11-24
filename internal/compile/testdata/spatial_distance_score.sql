-- field: allPlaces
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $5), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'locationDistance', ST_Distance(__t_1."location", CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), 4326) ELSE ST_GeomFromEWKT($1) END), 'metres', ST_Distance((__t_1."location")::geography, (CASE WHEN left($2, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($2), 4326) ELSE ST_GeomFromEWKT($2) END)::geography), 'areaDistance', ST_Distance(__t_1."area", (CASE WHEN left($3, 1) = '{' THEN ST_GeomFromGeoJSON($3) ELSE ST_GeomFromEWKT($3) END)::geography)) AS ndata, row_number() OVER (ORDER BY __t_1."location" <-> CASE WHEN left($4, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($4), 4326) ELSE ST_GeomFromEWKT($4) END ASC, __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  ORDER BY __t_1."location" <-> CASE WHEN left($4, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($4), 4326) ELSE ST_GeomFromEWKT($4) END ASC, __t_1."id" ASC
  LIMIT 4
) AS __s_2
GROUP BY ()
-- args: ["SRID=4326;POINT(-0.12 51.5)","SRID=4326;POINT(-0.12 51.5)","SRID=4326;POINT(-0.12 51.5)","SRID=4326;POINT(-0.12 51.5)",3]

-- field: allPlaces
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('locationDistance', ST_Distance(__t_1."location", CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), 4326) ELSE ST_GeomFromEWKT($1) END)) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: ["{\"coordinates\":[-0.12,51.5],\"type\":\"Point\"}",100]

-- field: allPlaces
-- error: compile: field locationDistance requires near: {location: …}

-- field: allPlaces
SELECT json_build_object('edges', coalesce(json_agg(json_build_object('node', __s_2.edata) ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('locationDistance', ST_Distance(__t_1."location", CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), 4326) ELSE ST_GeomFromEWKT($1) END)) AS edata, replace(encode(convert_to(jsonb_build_array('Place'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', '') AS __cursor, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT 3
) AS __s_2
GROUP BY ()
-- args: ["SRID=4326;POINT(-0.12 51.5)",2]

-- field: userById
SELECT json_build_object('placesByOwnerId', __l_4.data) AS data
FROM "public"."users" AS __t_1
LEFT JOIN LATERAL (
  SELECT json_build_object('nodes', coalesce(json_agg(__s_3.ndata ORDER BY __s_3.__rn ASC) FILTER (WHERE __s_3.__rn <= $3), '[]'::json)) AS data
  FROM (
    SELECT json_build_object('name', __t_2."name", 'areaDistance', ST_Distance(__t_2."area", (CASE WHEN left($1, 1) = '{' THEN ST_GeomFromGeoJSON($1) ELSE ST_GeomFromEWKT($1) END)::geography)) AS ndata, row_number() OVER (ORDER BY __t_2."area" <-> (CASE WHEN left($2, 1) = '{' THEN ST_GeomFromGeoJSON($2) ELSE ST_GeomFromEWKT($2) END)::geography ASC, __t_2."id" ASC) AS __rn
    FROM "public"."places" AS __t_2
    WHERE __t_2."owner_id" = __t_1."id"
    ORDER BY __t_2."area" <-> (CASE WHEN left($2, 1) = '{' THEN ST_GeomFromGeoJSON($2) ELSE ST_GeomFromEWKT($2) END)::geography ASC, __t_2."id" ASC
    LIMIT 3
  ) AS __s_3
  GROUP BY ()
) AS __l_4 ON true
WHERE __t_1."id" = $4
LIMIT 1
-- args: ["SRID=4326;POINT(0 0)","SRID=4326;POINT(0 0)",2,1]

-- field: placeById
SELECT json_build_object('locationDistance', NULL::float8) AS data
FROM "public"."places" AS __t_1
WHERE __t_1."id" = $1
LIMIT 1
-- args: [1]

