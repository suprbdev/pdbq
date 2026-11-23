-- field: allLocations
SELECT json_build_object('edges', coalesce(json_agg(json_build_object('cursor', __s_2.__cursor, 'node', __s_2.edata) ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $7), '[]'::json), 'pageInfo', json_build_object('hasNextPage', (count(*) > $7))) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'spotEarthDistance', "ext".earth_distance((__t_1."spot"), "ext".ll_to_earth($1::float8, $2::float8))) AS edata, replace(encode(convert_to(jsonb_build_array('Location'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', '') AS __cursor, row_number() OVER (ORDER BY (__t_1."spot") OPERATOR("ext".<->) "ext".ll_to_earth($3::float8, $4::float8) ASC, __t_1."id" ASC) AS __rn
  FROM "public"."locations" AS __t_1
  WHERE (((__t_1."spot") OPERATOR("ext".<->) "ext".ll_to_earth($3::float8, $4::float8) > (SELECT (__a."spot") OPERATOR("ext".<->) "ext".ll_to_earth($3::float8, $4::float8) FROM "public"."locations" AS __a WHERE __a."id" = $5) OR ((__t_1."spot") OPERATOR("ext".<->) "ext".ll_to_earth($3::float8, $4::float8) IS NULL AND (SELECT (__a."spot") OPERATOR("ext".<->) "ext".ll_to_earth($3::float8, $4::float8) FROM "public"."locations" AS __a WHERE __a."id" = $5) IS NOT NULL)) OR ((__t_1."spot") OPERATOR("ext".<->) "ext".ll_to_earth($3::float8, $4::float8) IS NOT DISTINCT FROM (SELECT (__a."spot") OPERATOR("ext".<->) "ext".ll_to_earth($3::float8, $4::float8) FROM "public"."locations" AS __a WHERE __a."id" = $5) AND __t_1."id" > $6))
  ORDER BY (__t_1."spot") OPERATOR("ext".<->) "ext".ll_to_earth($3::float8, $4::float8) ASC, __t_1."id" ASC
  LIMIT 3
) AS __s_2
GROUP BY ()
-- args: [51.5,-0.12,51.5,-0.12,"3","3",2]

