-- field: allDurations
SELECT json_build_object('aggregates', (SELECT json_build_object('min', json_build_object('span', (SELECT json_build_object('hours', extract(hour FROM __iv_4.i)::bigint) FROM (SELECT min(__a_3."span") AS i) AS __iv_4 WHERE __iv_4.i IS NOT NULL)), 'max', json_build_object('span', (SELECT json_build_object('hours', extract(hour FROM __iv_5.i)::bigint, 'days', extract(day FROM __iv_5.i)::bigint) FROM (SELECT max(__a_3."span") AS i) AS __iv_5 WHERE __iv_5.i IS NOT NULL)), 'sum', json_build_object('span', (SELECT json_build_object('hours', extract(hour FROM __iv_6.i)::bigint) FROM (SELECT coalesce(sum(__a_3."span"), '0') AS i) AS __iv_6 WHERE __iv_6.i IS NOT NULL))) FROM "public"."durations" AS __a_3)) AS data
FROM (
  SELECT row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."durations" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT $1
) AS __s_2
GROUP BY ()
-- args: [101]

