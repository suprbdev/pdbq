-- field: allPlaces
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('locationX', CASE WHEN ST_GeometryType(__t_1."location") = 'ST_Point' THEN ST_X(__t_1."location") END, 'locationY', CASE WHEN ST_GeometryType(__t_1."location") = 'ST_Point' THEN ST_Y(__t_1."location") END, 'locationNumPoints', ST_NPoints(__t_1."location"), 'locationNumGeometries', ST_NumGeometries(__t_1."location"), 'locationPointOnSurface', ST_AsGeoJSON(ST_PointOnSurface(__t_1."location"))::jsonb, 'locationWkt', ST_AsText(__t_1."location"), 'locationEwkt', ST_AsEWKT(__t_1."location"), 'locationIsEmpty', ST_IsEmpty(__t_1."location"), 'locationIsSimple', ST_IsSimple(__t_1."location"), 'locationXmin', ST_XMin(__t_1."location"), 'locationYmin', ST_YMin(__t_1."location"), 'locationXmax', ST_XMax(__t_1."location"), 'locationYmax', ST_YMax(__t_1."location"), 'areaX', CASE WHEN ST_GeometryType(__t_1."area"::geometry) = 'ST_Point' THEN ST_X(__t_1."area"::geometry) END, 'areaZ', CASE WHEN ST_GeometryType(__t_1."area"::geometry) = 'ST_Point' THEN ST_Z(__t_1."area"::geometry) END, 'areaM', CASE WHEN ST_GeometryType(__t_1."area"::geometry) = 'ST_Point' THEN ST_M(__t_1."area"::geometry) END, 'areaStartPoint', ST_AsGeoJSON(CASE WHEN ST_GeometryType(__t_1."area"::geometry) IN ('ST_LineString', 'ST_CircularString', 'ST_CompoundCurve') THEN ST_StartPoint(__t_1."area"::geometry) END)::jsonb, 'areaEndPoint', ST_AsGeoJSON(CASE WHEN ST_GeometryType(__t_1."area"::geometry) IN ('ST_LineString', 'ST_CircularString', 'ST_CompoundCurve') THEN ST_EndPoint(__t_1."area"::geometry) END)::jsonb, 'areaPointOnSurface', ST_AsGeoJSON(ST_PointOnSurface(__t_1."area"::geometry))::jsonb, 'areaWkt', ST_AsText(__t_1."area"), 'areaXmin', ST_XMin(__t_1."area"::geometry)) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT $1
) AS __s_2
GROUP BY ()
-- args: [101,100]

