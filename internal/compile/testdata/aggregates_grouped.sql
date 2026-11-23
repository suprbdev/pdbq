-- field: allPosts
SELECT jsonb_build_object('totalCount', (SELECT count(*) FROM "public"."posts" AS __c_3
WHERE __c_3."title" <> $3)::int, 'byAuthor', (SELECT coalesce(jsonb_agg(__g_5.data ORDER BY __g_5.__k1), '[]'::jsonb) FROM (
  SELECT jsonb_build_object('keys', jsonb_build_array(min((to_jsonb(__a_4."author_id") #>> '{}') COLLATE "C")), 'sum', jsonb_build_object('id', (coalesce(sum(__a_4."id"), '0'))::text), 'distinctCount', jsonb_build_object('title', (count(DISTINCT __a_4."title"))::text)) AS data, __a_4."author_id" AS __k1
  FROM "public"."posts" AS __a_4
  WHERE __a_4."title" <> $4
  GROUP BY __a_4."author_id"
  HAVING (count(DISTINCT __a_4."title") > $5 OR max(__a_4."id") < $6) AND coalesce(sum(__a_4."id"), '0') > $7
  ORDER BY __k1
  LIMIT $8
) AS __g_5)) AS data
FROM (
  SELECT row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."posts" AS __t_1
  WHERE __t_1."title" <> $1
  ORDER BY __t_1."id" ASC
  LIMIT $2
) AS __s_2
GROUP BY ()
-- args: ["x",101,"x","x","0",10,"1",100]

-- field: allUsers
SELECT jsonb_build_object('byDay', (SELECT coalesce(jsonb_agg(__g_4.data ORDER BY __g_4.__k1, __g_4.__k2), '[]'::jsonb) FROM (
  SELECT jsonb_build_object('keys', jsonb_build_array(min((to_jsonb(date_trunc('day', __a_3."created_at")) #>> '{}') COLLATE "C"), min((to_jsonb(CASE __a_3."mood" WHEN 'happy' THEN 'HAPPY' WHEN 'ok' THEN 'OK' WHEN 'sad' THEN 'SAD' END) #>> '{}') COLLATE "C")), 'sum', jsonb_build_object('balance', (coalesce(sum(__a_3."balance"), '0'))::text)) AS data, date_trunc('day', __a_3."created_at") AS __k1, __a_3."mood" AS __k2
  FROM "public"."users" AS __a_3
  GROUP BY date_trunc('day', __a_3."created_at"), __a_3."mood"
  ORDER BY __k1, __k2
  LIMIT $2
) AS __g_4), 'byMoods', (SELECT coalesce(jsonb_agg(__g_6.data ORDER BY __g_6.__k1, __g_6.__k2), '[]'::jsonb) FROM (
  SELECT jsonb_build_object('keys', jsonb_build_array(min((CASE WHEN __a_5."moods" IS NULL THEN NULL ELSE (SELECT coalesce(jsonb_agg(CASE (__a_5."moods")[__m_7.o] WHEN 'happy' THEN 'HAPPY' WHEN 'ok' THEN 'OK' WHEN 'sad' THEN 'SAD' END ORDER BY __m_7.o), '[]'::jsonb) FROM (SELECT generate_subscripts(__a_5."moods", 1) AS o) AS __m_7) END::text) COLLATE "C"), min((to_jsonb(__a_5."tags")::text) COLLATE "C"))) AS data, __a_5."moods" AS __k1, __a_5."tags" AS __k2
  FROM "public"."users" AS __a_5
  GROUP BY __a_5."moods", __a_5."tags"
  ORDER BY __k1, __k2
  LIMIT $3
) AS __g_6), 'byHour', (SELECT coalesce(jsonb_agg(__g_9.data ORDER BY __g_9.__k1), '[]'::jsonb) FROM (
  SELECT jsonb_build_object('keys', jsonb_build_array(min((to_jsonb(date_trunc('hour', __a_8."created_at")) #>> '{}') COLLATE "C"))) AS data, date_trunc('hour', __a_8."created_at") AS __k1
  FROM "public"."users" AS __a_8
  GROUP BY date_trunc('hour', __a_8."created_at")
  HAVING avg(__a_8."id") >= $4
  ORDER BY __k1
  LIMIT $5
) AS __g_9), 'bySettings', (SELECT coalesce(jsonb_agg(__g_11.data ORDER BY __g_11.__k1), '[]'::jsonb) FROM (
  SELECT jsonb_build_object('keys', jsonb_build_array(min((to_jsonb(__a_10."settings")::text) COLLATE "C"))) AS data, __a_10."settings" AS __k1
  FROM "public"."users" AS __a_10
  GROUP BY __a_10."settings"
  ORDER BY __k1
  LIMIT $6
) AS __g_11)) AS data
FROM (
  SELECT row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."users" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT $1
) AS __s_2
GROUP BY ()
-- args: [101,100,100,"1.5",100,100]

