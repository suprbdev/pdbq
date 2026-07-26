-- field: allPlaces
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('name', __t_1."name", 'location', CASE WHEN __t_1."location" IS NULL THEN NULL ELSE ST_AsGeoJSON(__t_1."location")::jsonb END, 'locationSrid', ST_SRID(__t_1."location"), 'locationGeometryType', ST_GeometryType(__t_1."location"), 'locationCentroid', CASE WHEN ST_Centroid(__t_1."location") IS NULL THEN NULL ELSE ST_AsGeoJSON(ST_Centroid(__t_1."location"))::jsonb END, 'locationEnvelope', CASE WHEN ST_Envelope(__t_1."location") IS NULL THEN NULL ELSE ST_AsGeoJSON(ST_Envelope(__t_1."location"))::jsonb END, 'locationIsValid', ST_IsValid(__t_1."location"), 'area', CASE WHEN __t_1."area" IS NULL THEN NULL ELSE ST_AsGeoJSON(__t_1."area")::jsonb END, 'areaArea', ST_Area(__t_1."area"), 'areaLength', ST_Length(__t_1."area"), 'areaCentroid', CASE WHEN ST_Centroid(__t_1."area"::geometry) IS NULL THEN NULL ELSE ST_AsGeoJSON(ST_Centroid(__t_1."area"::geometry))::jsonb END) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT $1
) AS __s_2
GROUP BY ()
-- args: [101,100]

-- field: allPlaces
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('webMercator', CASE WHEN ST_Transform(__t_1."location", 3857) IS NULL THEN NULL ELSE ST_AsGeoJSON(ST_Transform(__t_1."location", 3857))::jsonb END, 'coarse', CASE WHEN ST_SimplifyPreserveTopology(__t_1."location", $1) IS NULL THEN NULL ELSE ST_AsGeoJSON(ST_SimplifyPreserveTopology(__t_1."location", $1))::jsonb END, 'both', CASE WHEN ST_Transform(ST_SimplifyPreserveTopology(__t_1."location", $2), 3857) IS NULL THEN NULL ELSE ST_AsGeoJSON(ST_Transform(ST_SimplifyPreserveTopology(__t_1."location", $2), 3857))::jsonb END) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: [0.01,0.01,101,100]

