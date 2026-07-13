-- field: allUsers
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $2), '[]'::jsonb)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id", 'fullName', __t_1."full_name", 'postCount', ("public"."users_post_count"(ROW(__t_1."id", __t_1."email", __t_1."full_name", __t_1."mood", __t_1."settings", __t_1."tags", __t_1."balance", __t_1."created_at", __t_1."address", __t_1."prev_addresses")::"public"."users"))::text) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."users" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT $1
) AS __s_2
GROUP BY ()
-- args: [3,2]

-- field: postById
SELECT jsonb_build_object('id', __t_1."id", 'excerpt', "public"."posts_excerpt"(ROW(__t_1."id", __t_1."author_id", __t_1."title", __t_1."body", __t_1."published")::"public"."posts", $1::"int4")) AS data
FROM "public"."posts" AS __t_1
WHERE __t_1."id" = $2
LIMIT 1
-- args: [10,1]

