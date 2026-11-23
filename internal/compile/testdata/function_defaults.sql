-- field: greet
SELECT to_json(__fn.v) AS data FROM (SELECT "public"."greet"("name" => $1::"pg_catalog"."text") AS v) AS __fn
-- args: ["Ada"]

-- field: greet
SELECT to_json(__fn.v) AS data FROM (SELECT "public"."greet"("name" => $1::"pg_catalog"."text", "greeting" => $2::"pg_catalog"."text") AS v) AS __fn
-- args: ["Ada","Hi"]

-- field: greet
SELECT to_json(__fn.v) AS data FROM (SELECT "public"."greet"("name" => $1::"pg_catalog"."text", "greeting" => NULL::"pg_catalog"."text") AS v) AS __fn
-- args: ["Ada"]

-- field: greet
SELECT to_json(__fn.v) AS data FROM (SELECT "public"."greet"("name" => $1::"pg_catalog"."text", "greeting" => NULL::"pg_catalog"."text") AS v) AS __fn
-- args: ["Ada"]

-- field: greet
SELECT to_json(__fn.v) AS data FROM (SELECT "public"."greet"("name" => $1::"pg_catalog"."text") AS v) AS __fn
-- args: ["Ada"]

-- field: allUsers
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::json)) AS data
FROM (
  SELECT json_build_object('greeting', "public"."users_greeting"("u" => ROW(__t_1."id", __t_1."email", __t_1."full_name", __t_1."mood", __t_1."settings", __t_1."tags", __t_1."balance", __t_1."created_at", __t_1."address", __t_1."prev_addresses", __t_1."moods")::"public"."users"), 'greetingWithPrefix', "public"."users_greeting"("u" => ROW(__t_1."id", __t_1."email", __t_1."full_name", __t_1."mood", __t_1."settings", __t_1."tags", __t_1."balance", __t_1."created_at", __t_1."address", __t_1."prev_addresses", __t_1."moods")::"public"."users", "prefix" => $1::"pg_catalog"."text")) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."users" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT 2
) AS __s_2
GROUP BY ()
-- args: ["Yo",1]

