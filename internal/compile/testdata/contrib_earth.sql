-- field: allLocations
SELECT json_build_object('nodes', coalesce(json_agg(__s_4.ndata ORDER BY __s_4.__rn ASC) FILTER (WHERE __s_4.__rn <= $10), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'spot', (SELECT json_build_object('lat', "ext".latitude(__ll_2.e), 'lon', "ext".longitude(__ll_2.e)) FROM (SELECT __t_1."spot" AS e) AS __ll_2 WHERE __ll_2.e IS NOT NULL), 'where', (SELECT json_build_object('__typename', 'LatLon'::text, 'latitude', "ext".latitude(__ll_3.e)) FROM (SELECT __t_1."home" AS e) AS __ll_3 WHERE __ll_3.e IS NOT NULL), 'spotEarthDistance', "ext".earth_distance((__t_1."spot"), "ext".ll_to_earth($1::float8, $2::float8)), 'homeEarthDistance', "ext".earth_distance((__t_1."home"), "ext".ll_to_earth($3::float8, $4::float8))) AS ndata, row_number() OVER (ORDER BY (__t_1."spot") OPERATOR("ext".<->) "ext".ll_to_earth($5::float8, $6::float8) ASC, __t_1."id" ASC) AS __rn
  FROM "public"."locations" AS __t_1
  WHERE ("ext".earth_box("ext".ll_to_earth($7::float8, $8::float8), $9::float8) OPERATOR("ext".@>) (__t_1."spot") AND "ext".earth_distance((__t_1."spot"), "ext".ll_to_earth($7::float8, $8::float8)) < $9::float8)
  ORDER BY (__t_1."spot") OPERATOR("ext".<->) "ext".ll_to_earth($5::float8, $6::float8) ASC, __t_1."id" ASC
  LIMIT 4
) AS __s_4
GROUP BY ()
-- args: [51.5,-0.12,48.85,2.35,51.5,-0.12,51.5,-0.12,10000,3]

-- field: allLocations
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $8), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'hereEarthDistanceMiles', (__t_1."here") OPERATOR("ext".<@>) point($2::float8, $1::float8)) AS ndata, row_number() OVER (ORDER BY (__t_1."here") OPERATOR("ext".<@>) point($4::float8, $3::float8) DESC NULLS LAST, __t_1."id" ASC) AS __rn
  FROM "public"."locations" AS __t_1
  WHERE ((__t_1."here") OPERATOR("ext".<@>) point($6::float8, $5::float8)) < $7::float8
  ORDER BY (__t_1."here") OPERATOR("ext".<@>) point($4::float8, $3::float8) DESC NULLS LAST, __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: [40.7,-74,40.7,-74,40.7,-74,50,100]

-- field: allLocations
-- error: compile: nearPoint: lat must be between -90 and 90

-- field: allLocations
-- error: compile: orderBy SPOT_EARTH_DISTANCE_ASC requires a nearPoint: reference for the same column

-- field: locationById
SELECT json_build_object('spot', (SELECT json_build_object('lat', "ext".latitude(__ll_2.e)) FROM (SELECT __t_1."spot" AS e) AS __ll_2 WHERE __ll_2.e IS NOT NULL), 'spotEarthDistance', NULL::float8) AS data
FROM "public"."locations" AS __t_1
WHERE __t_1."id" = $1
LIMIT 1
-- args: [1]

