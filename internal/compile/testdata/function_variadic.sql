-- field: sumAll
SELECT to_json((__fn.v)::text) AS data FROM (SELECT "public"."sum_all"($1::"pg_catalog"."int4", VARIADIC $2::"pg_catalog"."_int4") AS v) AS __fn
-- args: [2,[1,2,3]]

-- field: sumAll
SELECT to_json((__fn.v)::text) AS data FROM (SELECT "public"."sum_all"($1::"pg_catalog"."int4") AS v) AS __fn
-- args: [3]

-- field: sumAll
SELECT to_json((__fn.v)::text) AS data FROM (SELECT "public"."sum_all"() AS v) AS __fn
-- args: null

