-- field: addNumbers
SELECT json_build_object('__typename', 'AddNumbersPayload'::text, 'integer', to_json(__fn.v), 'clientMutationId', $3::text) AS data
FROM (SELECT "public"."add_numbers"("a" => $1::"pg_catalog"."int4", "b" => $2::"pg_catalog"."int4") AS v) AS __fn
-- args: [2,40,"cm-1"]

-- field: publishPost
SELECT json_build_object('post', CASE WHEN __t_1 IS NULL THEN NULL ELSE json_build_object('title', __t_1."title", 'author', __l_3.data) END, 'author', __l_5.data, 'postEdge', CASE WHEN __t_1 IS NULL THEN NULL ELSE json_build_object('__typename', 'PostEdge'::text, 'cursor', replace(encode(convert_to(jsonb_build_array('Post'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', ''), 'node', json_build_object('id', __t_1."id")) END, 'query', json_build_object('__typename', 'Query'::text, 'postById', (SELECT __q_7.data FROM (
  SELECT json_build_object('published', __t_6."published") AS data
  FROM "public"."posts" AS __t_6
  WHERE __t_6."id" = $2
  LIMIT 1
) AS __q_7))) AS data
FROM "public"."publish_post"("post_id" => $1::"pg_catalog"."int4") AS __t_1
LEFT JOIN LATERAL (
  SELECT json_build_object('email', __t_2."email") AS data
  FROM "public"."users" AS __t_2
  WHERE __t_2."id" = __t_1."author_id"
  LIMIT 1
) AS __l_3 ON true
LEFT JOIN LATERAL (
  SELECT json_build_object('email', __t_4."email") AS data
  FROM "public"."users" AS __t_4
  WHERE __t_4."id" = __t_1."author_id"
  LIMIT 1
) AS __l_5 ON true
-- args: [7,7]

-- field: unpublishPost
SELECT json_build_object('clientMutationId', $2::text, 'author', __l_3.data) AS data
FROM "public"."unpublish_post"("post_id" => $1::"pg_catalog"."int4") AS __t_1
LEFT JOIN LATERAL (
  SELECT json_build_object('id', __t_2."id") AS data
  FROM "public"."users" AS __t_2
  WHERE __t_2."id" = __t_1."author_id"
  LIMIT 1
) AS __l_3 ON true
-- args: [7,"cm-2"]

-- field: retryProbe
SELECT json_build_object('clientMutationId', NULL::text) AS data
FROM (SELECT "public"."retry_probe"() AS v) AS __fn
-- args: null

-- field: publishPosts
SELECT json_build_object('posts', __agg.data, 'query', json_build_object('allPosts', (SELECT __q_8.data FROM (
  SELECT json_build_object('totalCount', (SELECT count(*) FROM "public"."posts" AS __c_7)::int) AS data
  FROM (
    SELECT row_number() OVER (ORDER BY __t_5."id" ASC) AS __rn
    FROM "public"."posts" AS __t_5
    ORDER BY __t_5."id" ASC
    LIMIT $2
  ) AS __s_6
  GROUP BY ()
) AS __q_8))) AS data
FROM (SELECT coalesce(json_agg(__s_4.data), '[]'::json) AS data FROM (
  SELECT json_build_object('id', __t_1."id", 'author', __l_3.data) AS data
  FROM "public"."publish_posts"("post_ids" => $1::"pg_catalog"."_int4") AS __t_1
  LEFT JOIN LATERAL (
    SELECT json_build_object('email', __t_2."email") AS data
    FROM "public"."users" AS __t_2
    WHERE __t_2."id" = __t_1."author_id"
    LIMIT 1
  ) AS __l_3 ON true
) AS __s_4) AS __agg
-- args: [[1,2],2]

-- field: touchPost
SELECT json_build_object('query', json_build_object('userById', (SELECT __q_2.data FROM (
  SELECT json_build_object('email', __t_1."email") AS data
  FROM "public"."users" AS __t_1
  WHERE __t_1."id" = $2
  LIMIT 1
) AS __q_2))) AS data
FROM (SELECT "public"."touch_post"("post_id" => $1::"pg_catalog"."int4") AS v) AS __fn
-- args: [1,1]

-- field: bumpBalance
SELECT json_build_object('newBalance', to_json((__fn.v)::text)) AS data
FROM (SELECT "public"."bump_balance"("user_id" => $1::"pg_catalog"."int4") AS v) AS __fn
-- args: [1]

-- field: postTitleList
SELECT json_build_object('strings', to_json(__fn.v)) AS data
FROM (SELECT "public"."post_title_list"("post_ids" => $1::"pg_catalog"."_int4") AS v) AS __fn
-- args: [[1]]

