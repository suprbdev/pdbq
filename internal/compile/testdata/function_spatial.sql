-- field: recordVisit
SELECT jsonb_build_object('result', CASE WHEN __t_1 IS NULL THEN NULL ELSE jsonb_build_object('visitId', __t_1."visit_id", 'spot', ST_AsGeoJSON(__t_1."spot")::jsonb) END) AS data
FROM "public"."record_visit"("spot" => CASE WHEN left($1, 1) = '{' THEN ST_GeomFromGeoJSON($1) ELSE ST_GeomFromEWKT($1) END::"public"."geometry") AS __t_1
-- args: ["{\"coordinates\":[1,2],\"type\":\"Point\"}"]

-- field: recordVisit
SELECT jsonb_build_object('result', CASE WHEN __t_1 IS NULL THEN NULL ELSE jsonb_build_object('visitId', __t_1."visit_id") END) AS data
FROM "public"."record_visit"("spot" => CASE WHEN left($1, 1) = '{' THEN ST_GeomFromGeoJSON($1) ELSE ST_GeomFromEWKT($1) END::"public"."geometry", "note" => $2::"pg_catalog"."jsonb") AS __t_1
-- args: ["POINT(1 2)","{\"via\":\"app\"}"]

