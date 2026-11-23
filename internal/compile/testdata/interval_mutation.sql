-- field: createDuration
WITH __mut AS (
  INSERT INTO "public"."durations" ("label", "span", "spans", "timing")
  VALUES ($1, make_interval(years => $2::int, months => $3::int, days => $4::int, hours => $5::int, mins => $6::int, secs => $7::float8), coalesce((SELECT array_agg(make_interval(years => (__e_1.v->>'years')::int, months => (__e_1.v->>'months')::int, days => (__e_1.v->>'days')::int, hours => (__e_1.v->>'hours')::int, mins => (__e_1.v->>'minutes')::int, secs => (__e_1.v->>'seconds')::float8) ORDER BY __e_1.o) FROM jsonb_array_elements($8::jsonb) WITH ORDINALITY AS __e_1(v, o)), ARRAY[]::interval[]), jsonb_populate_record(NULL::"public"."timing", $9::jsonb))
  RETURNING "id", "span", "spans", "timing"
)
SELECT json_build_object('duration', __row.data) AS data
FROM (
  SELECT json_build_object('id', __t_2."id", 'span', (SELECT json_build_object('hours', extract(hour FROM __iv_3.i)::bigint, 'minutes', extract(minute FROM __iv_3.i)::bigint, 'seconds', extract(second FROM __iv_3.i)::float8) FROM (SELECT __t_2."span" AS i) AS __iv_3 WHERE __iv_3.i IS NOT NULL), 'spans', (SELECT CASE WHEN __iv_4.a IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(CASE WHEN __e_5.v IS NULL THEN NULL ELSE json_build_object('days', extract(day FROM __e_5.v)::bigint, 'months', extract(month FROM __e_5.v)::bigint) END ORDER BY __e_5.o), '[]'::json) FROM unnest(__iv_4.a) WITH ORDINALITY AS __e_5(v, o)) END FROM (SELECT __t_2."spans" AS a) AS __iv_4), 'timing', CASE WHEN (__t_2."timing")::text IS NULL THEN NULL ELSE json_build_object('span', (SELECT json_build_object('days', extract(day FROM __iv_6.i)::bigint) FROM (SELECT (__t_2."timing")."span" AS i) AS __iv_6 WHERE __iv_6.i IS NOT NULL)) END) AS data
  FROM __mut AS __t_2
) AS __row
-- args: ["x",0,0,0,1,90,0.25,"[{\"days\":2,\"hours\":0,\"minutes\":0,\"months\":0,\"seconds\":0,\"years\":0},{\"days\":0,\"hours\":0,\"minutes\":0,\"months\":1,\"seconds\":0,\"years\":0}]","{\"label\":\"t\",\"span\":\"0 years 0 months 3 days 0 hours 0 minutes 0 seconds\",\"spans\":\"{\\\"0 years 0 months 0 days 4 hours 0 minutes 0 seconds\\\"}\"}"]

-- field: updateDurationById
WITH __mut AS (
  UPDATE "public"."durations"
  SET "span" = make_interval(years => $1::int, months => $2::int, days => $3::int, hours => $4::int, mins => $5::int, secs => $6::float8)
  WHERE "public"."durations"."id" = $7
  RETURNING "span"
)
SELECT json_build_object('duration', __row.data) AS data
FROM (
  SELECT json_build_object('span', (SELECT json_build_object('years', extract(year FROM __iv_2.i)::bigint) FROM (SELECT __t_1."span" AS i) AS __iv_2 WHERE __iv_2.i IS NOT NULL)) AS data
  FROM __mut AS __t_1
) AS __row
-- args: [1,0,0,0,0,0,1]

