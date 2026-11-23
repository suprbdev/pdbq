-- field: allPlaces
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $8), '[]'::json)) AS data
FROM (
  SELECT json_build_object('compact', ST_AsGeoJSON(__t_1."location", $1::int4)::json, 'fromVar', ST_AsGeoJSON(__t_1."location", $2::int4)::json, 'buffered', ST_AsGeoJSON(ST_Buffer(__t_1."area", $3::float8))::json, 'everything', ST_AsGeoJSON(ST_Transform(ST_SimplifyPreserveTopology(ST_Buffer(__t_1."area", $4::float8)::geometry, $5), 3857), $6::int4)::json, 'bufferedPoint', ST_AsGeoJSON(ST_Transform(ST_Buffer(__t_1."location", $7::float8), 3857))::json, 'none', ST_AsGeoJSON(__t_1."location")::json) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."places" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: [6,6,500,500,0.001,2,0.01,100]

