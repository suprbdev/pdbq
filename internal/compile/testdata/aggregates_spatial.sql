-- field: allPlaces
SELECT json_build_object('aggregates', (SELECT json_build_object('stExtent', json_build_object('location', ST_AsGeoJSON(ST_SetSRID(ST_Extent(__a_3."location")::geometry, 4326))::json, 'area', ST_AsGeoJSON(ST_SetSRID(ST_Extent(__a_3."area"::geometry)::geometry, min(ST_SRID(__a_3."area"))))::json), 'stUnion', json_build_object('location', ST_AsGeoJSON(ST_Union(__a_3."location"))::json), 'stCollect', json_build_object('location', ST_AsGeoJSON(ST_Collect(__a_3."location"))::json), 'stCentroid', json_build_object('location', ST_AsGeoJSON(ST_Centroid(ST_Collect(__a_3."location")))::json, 'area', ST_AsGeoJSON(ST_Centroid(ST_Collect(__a_3."area"::geometry)))::json), 'distinctCount', json_build_object('location', (count(DISTINCT __a_3."location"))::text)) FROM "public"."places" AS __a_3
WHERE __a_3."owner_id" = $3)) AS data
FROM (
  SELECT row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  WHERE __t_1."owner_id" = $1
  ORDER BY __t_1."id" ASC
  LIMIT $2
) AS __s_2
GROUP BY ()
-- args: [1,101,1]

