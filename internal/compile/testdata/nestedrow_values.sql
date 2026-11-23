-- field: postArr
SELECT CASE WHEN __fn.v IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(CASE WHEN ((__fn.v)[__e_1.o])::text IS NULL THEN NULL ELSE json_build_object('__typename', 'PostRow'::text, 'id', ((__fn.v)[__e_1.o])."id", 'title', ((__fn.v)[__e_1.o])."title") END ORDER BY __e_1.o), '[]'::json) FROM (SELECT generate_subscripts(__fn.v, 1) AS o) AS __e_1) END AS data FROM (SELECT "public"."post_arr"() AS v) AS __fn
-- args: null

-- field: postTab
SELECT coalesce(json_agg(__s_2.data), '[]'::json) AS data
FROM (
  SELECT json_build_object('n', __t_1."n", 'p', CASE WHEN (__t_1."p")::text IS NULL THEN NULL ELSE json_build_object('id', (__t_1."p")."id", 'authorId', (__t_1."p")."author_id") END) AS data
  FROM "public"."post_tab"() AS __t_1
) AS __s_2
-- args: null

-- field: vaultBoxes
SELECT coalesce(json_agg(CASE WHEN (__v.v)::text IS NULL THEN NULL ELSE json_build_object('label', (__v.v)."label") END), '[]'::json) AS data FROM (SELECT "public"."vault_boxes"() AS v) AS __v
-- args: null

-- field: allHolders
SELECT json_build_object('nodes', coalesce(json_agg(__s_3.ndata ORDER BY __s_3.__rn ASC) FILTER (WHERE __s_3.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('box', CASE WHEN (__t_1."box")::text IS NULL THEN NULL ELSE json_build_object('label', (__t_1."box")."label", 'p', CASE WHEN ((__t_1."box")."p")::text IS NULL THEN NULL ELSE json_build_object('title', ((__t_1."box")."p")."title", 'published', ((__t_1."box")."p")."published") END) END, 'vbox', CASE WHEN (__t_1."vbox")::text IS NULL THEN NULL ELSE json_build_object('label', (__t_1."vbox")."label") END, 'crate', CASE WHEN (__t_1."crate")::text IS NULL THEN NULL ELSE json_build_object('box', CASE WHEN ((__t_1."crate")."box")::text IS NULL THEN NULL ELSE json_build_object('p', CASE WHEN (((__t_1."crate")."box")."p")::text IS NULL THEN NULL ELSE json_build_object('id', (((__t_1."crate")."box")."p")."id") END) END, 'vb', CASE WHEN ((__t_1."crate")."vb")::text IS NULL THEN NULL ELSE json_build_object('label', ((__t_1."crate")."vb")."label") END) END, 'postList', CASE WHEN __t_1."post_list" IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(CASE WHEN ((__t_1."post_list")[__e_2.o])::text IS NULL THEN NULL ELSE json_build_object('id', ((__t_1."post_list")[__e_2.o])."id", 'title', ((__t_1."post_list")[__e_2.o])."title") END ORDER BY __e_2.o), '[]'::json) FROM (SELECT generate_subscripts(__t_1."post_list", 1) AS o) AS __e_2) END, 'post', CASE WHEN (__t_1."post")::text IS NULL THEN NULL ELSE json_build_object('id', (__t_1."post")."id") END) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."holders" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_3
GROUP BY ()
-- args: [100]

-- field: userById
SELECT json_build_object('postArr', (SELECT CASE WHEN __c_2.v IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(CASE WHEN ((__c_2.v)[__e_3.o])::text IS NULL THEN NULL ELSE json_build_object('id', ((__c_2.v)[__e_3.o])."id") END ORDER BY __e_3.o), '[]'::json) FROM (SELECT generate_subscripts(__c_2.v, 1) AS o) AS __e_3) END FROM (SELECT "public"."users_post_arr"("u" => ROW(__t_1."id", __t_1."email", __t_1."full_name", __t_1."mood", __t_1."settings", __t_1."tags", __t_1."balance", __t_1."created_at", __t_1."address", __t_1."prev_addresses", __t_1."moods")::"public"."users") AS v) AS __c_2)) AS data
FROM "public"."users" AS __t_1
WHERE __t_1."id" = $1
LIMIT 1
-- args: [1]

