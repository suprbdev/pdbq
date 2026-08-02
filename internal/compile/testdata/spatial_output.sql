-- field: allPlaces
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('name', __t_1."name", 'location', ST_AsGeoJSON(__t_1."location")::jsonb, 'locationSrid', ST_SRID(__t_1."location"), 'locationGeometryType', ST_GeometryType(__t_1."location"), 'locationCentroid', ST_AsGeoJSON(ST_Centroid(__t_1."location"))::jsonb, 'locationEnvelope', ST_AsGeoJSON(ST_Envelope(__t_1."location"))::jsonb, 'locationIsValid', ST_IsValid(__t_1."location"), 'area', ST_AsGeoJSON(__t_1."area")::jsonb, 'areaArea', ST_Area(__t_1."area"), 'areaLength', ST_Length(__t_1."area"), 'areaCentroid', ST_AsGeoJSON(ST_Centroid(__t_1."area"::geometry))::jsonb) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT $1
) AS __s_2
GROUP BY ()
-- args: [101,100]

-- field: allPlaces
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('webMercator', ST_AsGeoJSON(ST_Transform(__t_1."location", 3857))::jsonb, 'coarse', ST_AsGeoJSON(ST_SimplifyPreserveTopology(__t_1."location", $1))::jsonb, 'both', ST_AsGeoJSON(ST_Transform(ST_SimplifyPreserveTopology(__t_1."location", $2), 3857))::jsonb) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: [0.01,0.01,101,100]

