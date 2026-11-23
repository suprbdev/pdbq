-- field: allDurations
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $7), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."durations" AS __t_1
  WHERE __t_1."span" > make_interval(years => $1::int, months => $2::int, days => $3::int, hours => $4::int, mins => $5::int, secs => $6::float8)
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: [0,0,0,2,30,0,100]

-- field: allDurations
SELECT json_build_object('nodes', coalesce(json_agg(__s_3.ndata ORDER BY __s_3.__rn ASC) FILTER (WHERE __s_3.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."durations" AS __t_1
  WHERE __t_1."span" = ANY(coalesce((SELECT array_agg(make_interval(years => (__e_2.v->>'years')::int, months => (__e_2.v->>'months')::int, days => (__e_2.v->>'days')::int, hours => (__e_2.v->>'hours')::int, mins => (__e_2.v->>'minutes')::int, secs => (__e_2.v->>'seconds')::float8) ORDER BY __e_2.o) FROM jsonb_array_elements($1::jsonb) WITH ORDINALITY AS __e_2(v, o)), ARRAY[]::interval[])) AND __t_1."span" IS NOT NULL
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_3
GROUP BY ()
-- args: ["[{\"days\":1,\"hours\":0,\"minutes\":0,\"months\":0,\"seconds\":0,\"years\":0},{\"days\":0,\"hours\":0,\"minutes\":0,\"months\":0,\"seconds\":1.5,\"years\":0}]",100]

-- field: allDurations
SELECT json_build_object('nodes', coalesce(json_agg(__s_3.ndata ORDER BY __s_3.__rn ASC) FILTER (WHERE __s_3.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."durations" AS __t_1
  WHERE __t_1."spans" @> coalesce((SELECT array_agg(make_interval(years => (__e_2.v->>'years')::int, months => (__e_2.v->>'months')::int, days => (__e_2.v->>'days')::int, hours => (__e_2.v->>'hours')::int, mins => (__e_2.v->>'minutes')::int, secs => (__e_2.v->>'seconds')::float8) ORDER BY __e_2.o) FROM jsonb_array_elements($1::jsonb) WITH ORDINALITY AS __e_2(v, o)), ARRAY[]::interval[])
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_3
GROUP BY ()
-- args: ["[{\"days\":1,\"hours\":0,\"minutes\":0,\"months\":0,\"seconds\":0,\"years\":0},{\"days\":0,\"hours\":0,\"minutes\":0,\"months\":0,\"seconds\":1.5,\"years\":0}]",100]

-- field: allDurations
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $7), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."span" DESC NULLS LAST, __t_1."id" ASC) AS __rn
  FROM "public"."durations" AS __t_1
  WHERE __t_1."span" = make_interval(years => $1::int, months => $2::int, days => $3::int, hours => $4::int, mins => $5::int, secs => $6::float8)
  ORDER BY __t_1."span" DESC NULLS LAST, __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: [1,0,0,0,0,0,100]

