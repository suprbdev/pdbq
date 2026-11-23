-- field: widen
SELECT (SELECT json_build_object('start', CASE WHEN lower(__rg_1.r) IS NULL THEN NULL ELSE json_build_object('value', lower(__rg_1.r), 'inclusive', lower_inc(__rg_1.r)) END, 'end', CASE WHEN upper(__rg_1.r) IS NULL THEN NULL ELSE json_build_object('value', upper(__rg_1.r)) END) FROM (SELECT __fn.v AS r) AS __rg_1 WHERE __rg_1.r IS NOT NULL) AS data FROM (SELECT "public"."widen"("r" => int4range($1::int4, $2::int4, $3::text)::"pg_catalog"."int4range", "by" => $4::"pg_catalog"."int4") AS v) AS __fn
-- args: ["1","5","[)",2]

-- field: withFlag
SELECT "ext".hstore_to_json(__fn.v) AS data FROM (SELECT "public"."with_flag"("h" => "ext".hstore($1::text[])::"ext"."hstore") AS v) AS __fn
-- args: [["a","1"]]

-- field: totalNights
SELECT to_json(__fn.v) AS data FROM (SELECT "public"."total_nights"(VARIADIC $1::text[]::"pg_catalog"."_int4range"::"pg_catalog"."_int4range") AS v) AS __fn
-- args: [["[1,3)","[5,9)"]]

