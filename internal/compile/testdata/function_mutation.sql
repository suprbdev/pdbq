-- field: addNumbers
SELECT json_build_object('__typename', 'AddNumbersPayload'::text, 'integer', to_json(__fn.v), 'clientMutationId', $3::text) AS data
FROM (SELECT "public"."add_numbers"("a" => $1::"pg_catalog"."int4", "b" => $2::"pg_catalog"."int4") AS v) AS __fn
-- args: [2,40,"cm-1"]

-- field: publishPost
SELECT coalesce(json_agg(__t_1::text), '[]'::json) AS data
FROM "public"."publish_post"("post_id" => $1::"pg_catalog"."int4") AS __t_1
-- args: [7]
-- then:
SELECT json_build_object('post', CASE WHEN __t_1 IS NULL THEN NULL ELSE json_build_object('title', __t_1."title", 'author', __l_4.data) END, 'author', __l_6.data, 'postEdge', CASE WHEN __t_1 IS NULL THEN NULL ELSE json_build_object('__typename', 'PostEdge'::text, 'cursor', replace(encode(convert_to(jsonb_build_array('Post'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', ''), 'node', json_build_object('id', __t_1."id")) END, 'query', json_build_object('__typename', 'Query'::text, 'postById', (SELECT __q_8.data FROM (
  SELECT json_build_object('published', __t_7."published") AS data
  FROM "public"."posts" AS __t_7
  WHERE __t_7."id" = $2
  LIMIT 1
) AS __q_8))) AS data
FROM json_array_elements_text($1::json) AS __r_2(v)
CROSS JOIN LATERAL (SELECT (__r_2.v::"public"."posts").*) AS __t_1
LEFT JOIN LATERAL (
  SELECT json_build_object('email', __t_3."email") AS data
  FROM "public"."users" AS __t_3
  WHERE __t_3."id" = __t_1."author_id"
  LIMIT 1
) AS __l_4 ON true
LEFT JOIN LATERAL (
  SELECT json_build_object('email', __t_5."email") AS data
  FROM "public"."users" AS __t_5
  WHERE __t_5."id" = __t_1."author_id"
  LIMIT 1
) AS __l_6 ON true
-- args: [7]

-- field: unpublishPost
SELECT coalesce(json_agg(__t_1::text), '[]'::json) AS data
FROM "public"."unpublish_post"("post_id" => $1::"pg_catalog"."int4") AS __t_1
-- args: [7]
-- then:
SELECT json_build_object('clientMutationId', $2::text, 'author', __l_4.data) AS data
FROM json_array_elements_text($1::json) AS __r_2(v)
CROSS JOIN LATERAL (SELECT (__r_2.v::"public"."posts").*) AS __t_1
LEFT JOIN LATERAL (
  SELECT json_build_object('id', __t_3."id") AS data
  FROM "public"."users" AS __t_3
  WHERE __t_3."id" = __t_1."author_id"
  LIMIT 1
) AS __l_4 ON true
-- args: ["cm-2"]

-- field: retryProbe
SELECT json_build_object('clientMutationId', NULL::text) AS data
FROM (SELECT "public"."retry_probe"() AS v) AS __fn
-- args: null

-- field: publishPosts
SELECT coalesce(json_agg(__t_1::text), '[]'::json) AS data
FROM "public"."publish_posts"("post_ids" => $1::"pg_catalog"."_int4") AS __t_1
-- args: [[1,2]]
-- then:
SELECT json_build_object('posts', (SELECT coalesce(json_agg(__s_5.data ORDER BY __s_5.n), '[]'::json) FROM (
  SELECT json_build_object('id', __t_1."id", 'author', __l_3.data) AS data, __r_4.n
  FROM json_array_elements_text($1::json) WITH ORDINALITY AS __r_4(v, n)
  CROSS JOIN LATERAL (SELECT (__r_4.v::"public"."posts").*) AS __t_1
  LEFT JOIN LATERAL (
    SELECT json_build_object('email', __t_2."email") AS data
    FROM "public"."users" AS __t_2
    WHERE __t_2."id" = __t_1."author_id"
    LIMIT 1
  ) AS __l_3 ON true
) AS __s_5), 'query', json_build_object('allPosts', (SELECT __q_9.data FROM (
  SELECT json_build_object('totalCount', (SELECT count(*) FROM "public"."posts" AS __c_8)::int) AS data
  FROM (
    SELECT row_number() OVER (ORDER BY __t_6."id" ASC) AS __rn
    FROM "public"."posts" AS __t_6
    ORDER BY __t_6."id" ASC
    LIMIT 2
  ) AS __s_7
  GROUP BY ()
) AS __q_9))) AS data
-- args: []

-- field: touchPost
SELECT '{}'::json AS data
FROM (SELECT "public"."touch_post"("post_id" => $1::"pg_catalog"."int4") AS v) AS __fn
-- args: [1]
-- then:
SELECT json_build_object('query', json_build_object('userById', (SELECT __q_2.data FROM (
  SELECT json_build_object('email', __t_1."email") AS data
  FROM "public"."users" AS __t_1
  WHERE __t_1."id" = $2
  LIMIT 1
) AS __q_2))) AS data
-- args: [1]

-- field: bumpBalance
SELECT json_build_object('newBalance', to_json((__fn.v)::text)) AS data
FROM (SELECT "public"."bump_balance"("user_id" => $1::"pg_catalog"."int4") AS v) AS __fn
-- args: [1]

-- field: postTitleList
SELECT json_build_object('strings', to_json(__fn.v)) AS data
FROM (SELECT "public"."post_title_list"("post_ids" => $1::"pg_catalog"."_int4") AS v) AS __fn
-- args: [[1]]

