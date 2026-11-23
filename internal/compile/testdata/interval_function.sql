-- field: doubleSpan
SELECT (SELECT json_build_object('hours', extract(hour FROM __iv_1.i)::bigint, 'seconds', extract(second FROM __iv_1.i)::float8) FROM (SELECT __fn.v AS i) AS __iv_1 WHERE __iv_1.i IS NOT NULL) AS data FROM (SELECT "public"."double_span"("span" => make_interval(years => $1::int, months => $2::int, days => $3::int, hours => $4::int, mins => $5::int, secs => $6::float8)::"pg_catalog"."interval") AS v) AS __fn
-- args: [0,0,0,1,0,2.5]

-- field: spanList
SELECT (SELECT CASE WHEN __iv_2.a IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(CASE WHEN __e_3.v IS NULL THEN NULL ELSE json_build_object('days', extract(day FROM __e_3.v)::bigint, 'minutes', extract(minute FROM __e_3.v)::bigint) END ORDER BY __e_3.o), '[]'::json) FROM unnest(__iv_2.a) WITH ORDINALITY AS __e_3(v, o)) END FROM (SELECT __fn.v AS a) AS __iv_2) AS data FROM (SELECT "public"."span_list"("spans" => coalesce((SELECT array_agg(make_interval(years => (__e_1.v->>'years')::int, months => (__e_1.v->>'months')::int, days => (__e_1.v->>'days')::int, hours => (__e_1.v->>'hours')::int, mins => (__e_1.v->>'minutes')::int, secs => (__e_1.v->>'seconds')::float8) ORDER BY __e_1.o) FROM jsonb_array_elements($1::jsonb) WITH ORDINALITY AS __e_1(v, o)), ARRAY[]::interval[])::"pg_catalog"."_interval") AS v) AS __fn
-- args: ["[{\"days\":1,\"hours\":0,\"minutes\":0,\"months\":0,\"seconds\":0,\"years\":0},{\"days\":0,\"hours\":0,\"minutes\":5,\"months\":0,\"seconds\":0,\"years\":0}]"]

