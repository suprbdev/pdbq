-- field: bestMood
SELECT to_jsonb(CASE __fn.v WHEN 'happy' THEN 'HAPPY' WHEN 'ok' THEN 'OK' WHEN 'sad' THEN 'SAD' END) AS data FROM (SELECT "public"."best_mood"() AS v) AS __fn
-- args: null

-- field: allMoods
SELECT CASE WHEN __fn.v IS NULL THEN NULL ELSE (SELECT coalesce(jsonb_agg(CASE (__fn.v)[__m_1.o] WHEN 'happy' THEN 'HAPPY' WHEN 'ok' THEN 'OK' WHEN 'sad' THEN 'SAD' END ORDER BY __m_1.o), '[]'::jsonb) FROM (SELECT generate_subscripts(__fn.v, 1) AS o) AS __m_1) END AS data FROM (SELECT "public"."all_moods"() AS v) AS __fn
-- args: null

-- field: moodMoods
SELECT coalesce(jsonb_agg(to_jsonb(CASE __v WHEN 'happy' THEN 'HAPPY' WHEN 'ok' THEN 'OK' WHEN 'sad' THEN 'SAD' END)), '[]'::jsonb) AS data FROM "public"."mood_moods"() AS __v
-- args: null

-- field: describeMood
SELECT to_jsonb(__fn.v) AS data FROM (SELECT "public"."describe_mood"("m" => $1::"public"."mood") AS v) AS __fn
-- args: ["happy"]

