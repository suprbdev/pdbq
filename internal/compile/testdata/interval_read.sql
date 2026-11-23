-- field: allDurations
SELECT json_build_object('nodes', coalesce(json_agg(__s_9.ndata ORDER BY __s_9.__rn ASC) FILTER (WHERE __s_9.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'span', (SELECT json_build_object('years', extract(year FROM __iv_2.i)::bigint, 'months', extract(month FROM __iv_2.i)::bigint, 'days', extract(day FROM __iv_2.i)::bigint, 'hours', extract(hour FROM __iv_2.i)::bigint, 'minutes', extract(minute FROM __iv_2.i)::bigint, 'seconds', extract(second FROM __iv_2.i)::float8, '__typename', 'Interval'::text) FROM (SELECT __t_1."span" AS i) AS __iv_2 WHERE __iv_2.i IS NOT NULL), 'spans', (SELECT CASE WHEN __iv_3.a IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(CASE WHEN __e_4.v IS NULL THEN NULL ELSE json_build_object('hours', extract(hour FROM __e_4.v)::bigint, 'seconds', extract(second FROM __e_4.v)::float8) END ORDER BY __e_4.o), '[]'::json) FROM unnest(__iv_3.a) WITH ORDINALITY AS __e_4(v, o)) END FROM (SELECT __t_1."spans" AS a) AS __iv_3), 'timing', CASE WHEN (__t_1."timing")::text IS NULL THEN NULL ELSE json_build_object('label', (__t_1."timing")."label", 'span', (SELECT json_build_object('days', extract(day FROM __iv_5.i)::bigint) FROM (SELECT (__t_1."timing")."span" AS i) AS __iv_5 WHERE __iv_5.i IS NOT NULL), 'spans', (SELECT CASE WHEN __iv_6.a IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(CASE WHEN __e_7.v IS NULL THEN NULL ELSE json_build_object('minutes', extract(minute FROM __e_7.v)::bigint) END ORDER BY __e_7.o), '[]'::json) FROM unnest(__iv_6.a) WITH ORDINALITY AS __e_7(v, o)) END FROM (SELECT (__t_1."timing")."spans" AS a) AS __iv_6)) END, 'twice', (SELECT json_build_object('hours', extract(hour FROM __iv_8.i)::bigint) FROM (SELECT "public"."durations_twice"("d" => ROW(__t_1."id", __t_1."label", __t_1."span", __t_1."spans", __t_1."timing")::"public"."durations") AS i) AS __iv_8 WHERE __iv_8.i IS NOT NULL)) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."durations" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT 3
) AS __s_9
GROUP BY ()
-- args: [2]

