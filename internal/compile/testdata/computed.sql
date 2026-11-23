-- field: allUsers
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'fullName', __t_1."full_name", 'postCount', ("public"."users_post_count"("u" => ROW(__t_1."id", __t_1."email", __t_1."full_name", __t_1."mood", __t_1."settings", __t_1."tags", __t_1."balance", __t_1."created_at", __t_1."address", __t_1."prev_addresses", __t_1."moods")::"public"."users"))::text) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."users" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT 3
) AS __s_2
GROUP BY ()
-- args: [2]

-- field: postById
SELECT json_build_object('id', __t_1."id", 'excerpt', "public"."posts_excerpt"("p" => ROW(__t_1."id", __t_1."author_id", __t_1."title", __t_1."body", __t_1."published")::"public"."posts", "max_chars" => $1::"pg_catalog"."int4")) AS data
FROM "public"."posts" AS __t_1
WHERE __t_1."id" = $2
LIMIT 1
-- args: [10,1]

