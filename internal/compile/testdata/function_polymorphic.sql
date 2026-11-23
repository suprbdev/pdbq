-- field: pickFirst
SELECT to_json(__fn.v) AS data FROM (SELECT "public"."pick_first"("a" => NULL::"pg_catalog"."text", "b" => $1::"pg_catalog"."text") AS v) AS __fn
-- args: ["fallback"]

-- field: largerOf
SELECT to_json((__fn.v)::text) AS data FROM (SELECT "public"."larger_of"("a" => $1::"pg_catalog"."numeric", "b" => $2::"pg_catalog"."numeric") AS v) AS __fn
-- args: ["1.5","2"]

-- field: rangeLower
SELECT to_json(__fn.v) AS data FROM (SELECT "public"."range_lower"("r" => int4range($1::int4, $2::int4, $3::text)::"pg_catalog"."int4range") AS v) AS __fn
-- args: ["3","9","[)"]

