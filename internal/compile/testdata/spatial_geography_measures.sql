-- field: allPlaces
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('areaArea', ST_Area(__t_1."area"), 'areaLength', ST_Length(__t_1."area"), 'areaPerimeter', ST_Perimeter(__t_1."area"), 'areaCentroid', ST_AsGeoJSON(CASE WHEN ST_GeometryType(__t_1."area"::geometry) = 'ST_GeometryCollection' THEN ST_Centroid(__t_1."area"::geometry) ELSE ST_Centroid(__t_1."area")::geometry END)::json) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: [100]

