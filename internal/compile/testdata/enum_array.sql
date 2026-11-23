-- field: userById
SELECT json_build_object('id', __t_1."id", 'mood', CASE __t_1."mood" WHEN 'happy' THEN 'HAPPY' WHEN 'ok' THEN 'OK' WHEN 'sad' THEN 'SAD' END, 'moods', CASE WHEN __t_1."moods" IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(CASE (__t_1."moods")[__m_2.o] WHEN 'happy' THEN 'HAPPY' WHEN 'ok' THEN 'OK' WHEN 'sad' THEN 'SAD' END ORDER BY __m_2.o), '[]'::json) FROM (SELECT generate_subscripts(__t_1."moods", 1) AS o) AS __m_2) END) AS data
FROM "public"."users" AS __t_1
WHERE __t_1."id" = $1
LIMIT 1
-- args: [1]

