-- field: allPlaces
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('name', __t_1."name", 'location', ST_AsGeoJSON(__t_1."location")::json, 'locationSrid', ST_SRID(__t_1."location"), 'locationGeometryType', ST_GeometryType(__t_1."location"), 'locationCentroid', ST_AsGeoJSON(ST_Centroid(__t_1."location"))::json, 'locationEnvelope', ST_AsGeoJSON(ST_Envelope(__t_1."location"))::json, 'locationIsValid', ST_IsValid(__t_1."location"), 'area', ST_AsGeoJSON(__t_1."area")::json, 'areaArea', ST_Area(__t_1."area"), 'areaLength', ST_Length(__t_1."area"), 'areaCentroid', ST_AsGeoJSON(ST_Centroid(__t_1."area"::geometry))::json) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: [100]

-- field: allPlaces
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('webMercator', ST_AsGeoJSON(ST_Transform(__t_1."location", 3857))::json, 'coarse', ST_AsGeoJSON(ST_SimplifyPreserveTopology(__t_1."location", $1))::json, 'both', ST_AsGeoJSON(ST_Transform(ST_SimplifyPreserveTopology(__t_1."location", $2), 3857))::json) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: [0.01,0.01,100]

