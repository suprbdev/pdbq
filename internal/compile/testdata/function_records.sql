-- field: postStats
SELECT json_build_object('total', (__t_1."total")::text, 'maxTitle', __t_1."max_title") AS data
FROM "public"."post_stats"("min_id" => $1::"pg_catalog"."int4") AS __t_1
WHERE NOT (__t_1 IS NULL)
LIMIT 1
-- args: [2]

-- field: postTitles
SELECT coalesce(json_agg(__s_2.data), '[]'::json) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'title', __t_1."title") AS data
  FROM "public"."post_titles"() AS __t_1
  LIMIT 100
) AS __s_2
-- args: null

-- field: makeAddress
SELECT CASE WHEN (__fn.v)::text IS NULL THEN NULL ELSE json_build_object('street', (__fn.v)."street", 'city', (__fn.v)."city", 'mood', CASE (__fn.v)."mood" WHEN 'happy' THEN 'HAPPY' WHEN 'ok' THEN 'OK' WHEN 'sad' THEN 'SAD' END) END AS data FROM (SELECT "public"."make_address"("street" => $1::"pg_catalog"."text", "city" => $2::"pg_catalog"."text") AS v) AS __fn
-- args: ["1 Main St","Springfield"]

-- field: formatAddress
SELECT to_json(__fn.v) AS data FROM (SELECT "public"."format_address"("a" => jsonb_populate_record(NULL::"public"."address", $1::jsonb)::"public"."address") AS v) AS __fn
-- args: ["{\"city\":\"Springfield\",\"mood\":\"happy\",\"street\":\"1 Main St\"}"]

-- field: userById
SELECT json_build_object('home', (SELECT CASE WHEN (__c_2.v)::text IS NULL THEN NULL ELSE json_build_object('city', (__c_2.v)."city") END FROM (SELECT "public"."users_home"("u" => ROW(__t_1."id", __t_1."email", __t_1."full_name", __t_1."mood", __t_1."settings", __t_1."tags", __t_1."balance", __t_1."created_at", __t_1."address", __t_1."prev_addresses", __t_1."moods")::"public"."users") AS v) AS __c_2)) AS data
FROM "public"."users" AS __t_1
WHERE __t_1."id" = $1
LIMIT 1
-- args: [1]

