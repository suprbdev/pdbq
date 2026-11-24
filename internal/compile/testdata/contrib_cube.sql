-- field: allLocations
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $15), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'pos', (SELECT CASE WHEN json_array_length(__cube.j) = 1 THEN __cube.j -> 0 ELSE __cube.j END FROM (SELECT ('[' || translate((__t_1."pos")::text, '()', '[]') || ']')::json) AS __cube(j)), 'box', (SELECT CASE WHEN json_array_length(__cube.j) = 1 THEN __cube.j -> 0 ELSE __cube.j END FROM (SELECT ('[' || translate((__t_1."box")::text, '()', '[]') || ']')::json) AS __cube(j)), 'posL2Distance', (__t_1."pos") OPERATOR("ext".<->) $1::"ext".cube, 'posL1Distance', (__t_1."pos") OPERATOR("ext".<#>) $2::"ext".cube, 'boxChebyshevDistance', (__t_1."box") OPERATOR("ext".<=>) $3::"ext".cube) AS ndata, row_number() OVER (ORDER BY (__t_1."pos") OPERATOR("ext".<->) $4::"ext".cube ASC, (__t_1."pos") OPERATOR("ext".<=>) $5::"ext".cube DESC NULLS LAST, __t_1."id" ASC) AS __rn
  FROM "public"."locations" AS __t_1
  WHERE __t_1."box" OPERATOR("ext".=) $6::"ext".cube AND __t_1."box" IS NOT NULL AND __t_1."box" OPERATOR("ext".<>) $7::"ext".cube AND ((__t_1."pos") OPERATOR("ext".&&) "ext".cube_enlarge($8::"ext".cube, $9::float8, "ext".cube_dim($8::"ext".cube)) AND ((__t_1."pos") OPERATOR("ext".<=>) $8::"ext".cube) < $9::float8) AND (__t_1."pos") OPERATOR("ext".<@) $10::"ext".cube AND (__t_1."pos") OPERATOR("ext".@>) $11::"ext".cube AND ((__t_1."pos") OPERATOR("ext".&&) "ext".cube_enlarge($12::"ext".cube, $13::float8, "ext".cube_dim($12::"ext".cube)) AND ((__t_1."pos") OPERATOR("ext".<->) $12::"ext".cube) < $13::float8) AND (__t_1."pos") OPERATOR("ext".&&) $14::"ext".cube
  ORDER BY (__t_1."pos") OPERATOR("ext".<->) $4::"ext".cube ASC, (__t_1."pos") OPERATOR("ext".<=>) $5::"ext".cube DESC NULLS LAST, __t_1."id" ASC
  LIMIT 6
) AS __s_2
GROUP BY ()
-- args: ["(1, 2)","(1, 2)","(0, 0),(1, 1)","(1, 2)","(1, 2)","(1, 2)","(0, 0),(1, 1)","(0, 0),(1, 1)",2,"(0, 0),(10, 10)","(1, 2)","(1, 2)",5,"(1, 1), (2, 2)",5]

-- field: allLocations
-- error: compile: orderBy POS_L1_DISTANCE_ASC requires near: {pos: …}

-- field: allLocations
-- error: compile: near: cube value must be a list of numbers, a pair of such lists or a cube literal string

