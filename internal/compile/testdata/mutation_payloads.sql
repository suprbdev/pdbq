-- field: createPost
WITH __mut AS (
  INSERT INTO "public"."posts" ("author_id", "title")
  VALUES ($1, $2)
  RETURNING "id", "author_id", "title"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "title" FROM "public"."posts"
  UNION ALL SELECT "id", "title" FROM __mut
)
SELECT json_build_object('clientMutationId', $3::text, 'userByAuthorId', __l_3.data, 'postEdge', json_build_object('cursor', replace(encode(convert_to(jsonb_build_array('Post'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', ''), 'node', json_build_object('title', __t_1."title")), 'query', json_build_object('allPosts', (SELECT __q_7.data FROM (
  SELECT json_build_object('totalCount', (SELECT count(*) FROM __vis AS __c_6)::int, 'nodes', coalesce(json_agg(__s_5.ndata ORDER BY __s_5.__rn ASC) FILTER (WHERE __s_5.__rn <= $4), '[]'::json)) AS data
  FROM (
    SELECT json_build_object('title', __t_4."title") AS ndata, row_number() OVER (ORDER BY __t_4."id" ASC) AS __rn
    FROM __vis AS __t_4
    ORDER BY __t_4."id" ASC
    LIMIT 11
  ) AS __s_5
  GROUP BY ()
) AS __q_7), 'userById', (SELECT __q_9.data FROM (
  SELECT json_build_object('email', __t_8."email") AS data
  FROM "public"."users" AS __t_8
  WHERE __t_8."id" = $5
  LIMIT 1
) AS __q_9))) AS data
FROM __mut AS __t_1
LEFT JOIN LATERAL (
  SELECT json_build_object('email', __t_2."email") AS data
  FROM "public"."users" AS __t_2
  WHERE __t_2."id" = __t_1."author_id"
  LIMIT 1
) AS __l_3 ON true
-- args: [1,"Hi","cm-1",10,1]

-- field: deletePostById
WITH __mut AS (
  DELETE FROM "public"."posts"
  WHERE "public"."posts"."id" = $1
  RETURNING "id"
),
__vis AS NOT MATERIALIZED (
  SELECT "id" FROM "public"."posts" WHERE ("public"."posts"."id" = $1) IS NOT TRUE
)
SELECT json_build_object('post', json_build_object('id', __t_1."id"), 'query', json_build_object('postById', (SELECT __q_3.data FROM (
  SELECT json_build_object('id', __t_2."id") AS data
  FROM __vis AS __t_2
  WHERE __t_2."id" = $2
  LIMIT 1
) AS __q_3))) AS data
FROM __mut AS __t_1
-- args: [3,3]

-- field: updateManyPosts
WITH __mut AS (
  UPDATE "public"."posts"
  SET "published" = $1
  WHERE "public"."posts"."author_id" = $2
  RETURNING "id", "author_id"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "author_id" FROM "public"."posts" WHERE NOT EXISTS (SELECT 1 FROM __mut AS __m_5 WHERE __m_5."id" = "public"."posts"."id")
  UNION ALL SELECT "id", "author_id" FROM __mut
)
SELECT json_build_object('affectedCount', (SELECT count(*) FROM __mut), 'query', json_build_object('allPosts', (SELECT __q_4.data FROM (
  SELECT json_build_object('totalCount', (SELECT count(*) FROM __vis AS __c_3
  WHERE __c_3."author_id" = $4)::int) AS data
  FROM (
    SELECT row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
    FROM __vis AS __t_1
    WHERE __t_1."author_id" = $3
    ORDER BY __t_1."id" ASC
    LIMIT 11
  ) AS __s_2
  GROUP BY ()
) AS __q_4))) AS data
-- args: [true,1,1,1]

