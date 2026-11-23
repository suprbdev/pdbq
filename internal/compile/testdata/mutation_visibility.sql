-- field: createPost
WITH __mut AS (
  INSERT INTO "public"."posts" ("author_id", "title")
  VALUES ($1, $2)
  RETURNING "id", "author_id", "title"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "author_id", "title" FROM "public"."posts"
  UNION ALL SELECT "id", "author_id", "title" FROM __mut
)
SELECT json_build_object('post', json_build_object('title', __t_1."title", 'author', __l_7.data)) AS data
FROM __mut AS __t_1
LEFT JOIN LATERAL (
  SELECT json_build_object('postsByAuthorId', __l_6.data) AS data
  FROM "public"."users" AS __t_2
  LEFT JOIN LATERAL (
    SELECT json_build_object('totalCount', (SELECT count(*) FROM __vis AS __c_5
    WHERE __c_5."author_id" = __t_2."id" AND __c_5."title" LIKE $5)::int, 'nodes', coalesce(json_agg(__s_4.ndata ORDER BY __s_4.__rn ASC) FILTER (WHERE __s_4.__rn <= $4), '[]'::json)) AS data
    FROM (
      SELECT json_build_object('title', __t_3."title") AS ndata, row_number() OVER (ORDER BY __t_3."id" ASC) AS __rn
      FROM __vis AS __t_3
      WHERE __t_3."author_id" = __t_2."id" AND __t_3."title" LIKE $3
      ORDER BY __t_3."id" ASC
      LIMIT 101
    ) AS __s_4
    GROUP BY ()
  ) AS __l_6 ON true
  WHERE __t_2."id" = __t_1."author_id"
  LIMIT 1
) AS __l_7 ON true
-- args: [1,"New","N%",100,"N%"]

-- field: createComment
WITH __mut AS (
  INSERT INTO "public"."comments" ("body", "parent_id", "post_id")
  VALUES ($1, $2, $3)
  RETURNING "id", "parent_id", "body"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "parent_id", "body" FROM "public"."comments"
  UNION ALL SELECT "id", "parent_id", "body" FROM __mut
)
SELECT json_build_object('comment', json_build_object('body', __t_1."body", 'parent', __l_7.data)) AS data
FROM __mut AS __t_1
LEFT JOIN LATERAL (
  SELECT json_build_object('body', __t_2."body", 'commentsByParentId', __l_6.data) AS data
  FROM __vis AS __t_2
  LEFT JOIN LATERAL (
    SELECT json_build_object('totalCount', (SELECT count(*) FROM __vis AS __c_5
    WHERE __c_5."parent_id" = __t_2."id")::int) AS data
    FROM (
      SELECT row_number() OVER (ORDER BY __t_3."id" ASC) AS __rn
      FROM __vis AS __t_3
      WHERE __t_3."parent_id" = __t_2."id"
      ORDER BY __t_3."id" ASC
      LIMIT 101
    ) AS __s_4
    GROUP BY ()
  ) AS __l_6 ON true
  WHERE __t_2."id" = __t_1."parent_id"
  LIMIT 1
) AS __l_7 ON true
-- args: ["Reply",3,1]

-- field: updatePostById
WITH __mut AS (
  UPDATE "public"."posts"
  SET "title" = $1
  WHERE "public"."posts"."id" = $2
  RETURNING "id", "author_id", "title"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "author_id", "title" FROM "public"."posts" WHERE ("public"."posts"."id" = $2) IS NOT TRUE
  UNION ALL SELECT "id", "author_id", "title" FROM __mut
)
SELECT json_build_object('post', json_build_object('id', __t_1."id", 'author', __l_6.data)) AS data
FROM __mut AS __t_1
LEFT JOIN LATERAL (
  SELECT json_build_object('postsByAuthorId', __l_5.data) AS data
  FROM "public"."users" AS __t_2
  LEFT JOIN LATERAL (
    SELECT json_build_object('nodes', coalesce(json_agg(__s_4.ndata ORDER BY __s_4.__rn ASC) FILTER (WHERE __s_4.__rn <= $3), '[]'::json)) AS data
    FROM (
      SELECT json_build_object('id', __t_3."id", 'title', __t_3."title") AS ndata, row_number() OVER (ORDER BY __t_3."title" ASC, __t_3."id" ASC) AS __rn
      FROM __vis AS __t_3
      WHERE __t_3."author_id" = __t_2."id"
      ORDER BY __t_3."title" ASC, __t_3."id" ASC
      LIMIT 101
    ) AS __s_4
    GROUP BY ()
  ) AS __l_5 ON true
  WHERE __t_2."id" = __t_1."author_id"
  LIMIT 1
) AS __l_6 ON true
-- args: ["Renamed",1,100]

-- field: updateUserByEmail
WITH __mut AS (
  UPDATE "public"."users"
  SET "email" = $1
  WHERE "public"."users"."email" = $2
  RETURNING "id", "email"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "email" FROM "public"."users" WHERE ("public"."users"."email" = $2) IS NOT TRUE
  UNION ALL SELECT "id", "email" FROM __mut
)
SELECT json_build_object('user', json_build_object('email', __t_1."email", 'postsByAuthorId', __l_6.data)) AS data
FROM __mut AS __t_1
LEFT JOIN LATERAL (
  SELECT json_build_object('nodes', coalesce(json_agg(__s_5.ndata ORDER BY __s_5.__rn ASC) FILTER (WHERE __s_5.__rn <= $3), '[]'::json)) AS data
  FROM (
    SELECT json_build_object('author', __l_4.data) AS ndata, __t_2.__rn
    FROM (
      SELECT __t_2."author_id", row_number() OVER (ORDER BY __t_2."id" ASC) AS __rn
      FROM "public"."posts" AS __t_2
      WHERE __t_2."author_id" = __t_1."id"
      ORDER BY __t_2."id" ASC
      LIMIT 101
    ) AS __t_2
    LEFT JOIN LATERAL (
      SELECT json_build_object('email', __t_3."email") AS data
      FROM __vis AS __t_3
      WHERE __t_3."id" = __t_2."author_id"
      LIMIT 1
    ) AS __l_4 ON true
  ) AS __s_5
  GROUP BY ()
) AS __l_6 ON true
-- args: ["z@b.c","a@b.c",100]

-- field: upsertUserByEmail
WITH __mut AS (
  INSERT INTO "public"."users" ("email", "full_name")
  VALUES ($1, $2)
  ON CONFLICT ("email") DO UPDATE
  SET "full_name" = EXCLUDED."full_name"
  RETURNING "id", "email", "full_name"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "full_name" FROM "public"."users" WHERE ("public"."users"."email" = $1) IS NOT TRUE
  UNION ALL SELECT "id", "full_name" FROM __mut
)
SELECT json_build_object('user', json_build_object('email', __t_1."email", 'postsByAuthorId', __l_6.data)) AS data
FROM __mut AS __t_1
LEFT JOIN LATERAL (
  SELECT json_build_object('nodes', coalesce(json_agg(__s_5.ndata ORDER BY __s_5.__rn ASC) FILTER (WHERE __s_5.__rn <= $3), '[]'::json)) AS data
  FROM (
    SELECT json_build_object('author', __l_4.data) AS ndata, __t_2.__rn
    FROM (
      SELECT __t_2."author_id", row_number() OVER (ORDER BY __t_2."id" ASC) AS __rn
      FROM "public"."posts" AS __t_2
      WHERE __t_2."author_id" = __t_1."id"
      ORDER BY __t_2."id" ASC
      LIMIT 101
    ) AS __t_2
    LEFT JOIN LATERAL (
      SELECT json_build_object('fullName', __t_3."full_name") AS data
      FROM __vis AS __t_3
      WHERE __t_3."id" = __t_2."author_id"
      LIMIT 1
    ) AS __l_4 ON true
  ) AS __s_5
  GROUP BY ()
) AS __l_6 ON true
-- args: ["a@b.c","Ada",100]

-- field: deletePostById
WITH __mut AS (
  DELETE FROM "public"."posts"
  WHERE "public"."posts"."id" = $1
  RETURNING "author_id", "title"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "author_id" FROM "public"."posts" WHERE ("public"."posts"."id" = $1) IS NOT TRUE
)
SELECT json_build_object('post', json_build_object('title', __t_1."title", 'author', __l_7.data)) AS data
FROM __mut AS __t_1
LEFT JOIN LATERAL (
  SELECT json_build_object('postsByAuthorId', __l_6.data) AS data
  FROM "public"."users" AS __t_2
  LEFT JOIN LATERAL (
    SELECT json_build_object('totalCount', (SELECT count(*) FROM __vis AS __c_5
    WHERE __c_5."author_id" = __t_2."id")::int) AS data
    FROM (
      SELECT row_number() OVER (ORDER BY __t_3."id" ASC) AS __rn
      FROM __vis AS __t_3
      WHERE __t_3."author_id" = __t_2."id"
      ORDER BY __t_3."id" ASC
      LIMIT 101
    ) AS __s_4
    GROUP BY ()
  ) AS __l_6 ON true
  WHERE __t_2."id" = __t_1."author_id"
  LIMIT 1
) AS __l_7 ON true
-- args: [1]

-- field: createPosts
WITH __mut AS (
  INSERT INTO "public"."posts" ("author_id", "title")
  VALUES ($1, $2),
         ($3, $4)
  RETURNING "id", "author_id"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "author_id" FROM "public"."posts"
  UNION ALL SELECT "id", "author_id" FROM __mut
)
SELECT json_build_object('posts', (SELECT coalesce(json_agg(__s_8.data), '[]'::json) FROM (
  SELECT json_build_object('author', __l_7.data) AS data
  FROM __mut AS __t_1
  LEFT JOIN LATERAL (
    SELECT json_build_object('postsByAuthorId', __l_6.data) AS data
    FROM "public"."users" AS __t_2
    LEFT JOIN LATERAL (
      SELECT json_build_object('totalCount', (SELECT count(*) FROM __vis AS __c_5
      WHERE __c_5."author_id" = __t_2."id")::int) AS data
      FROM (
        SELECT row_number() OVER (ORDER BY __t_3."id" ASC) AS __rn
        FROM __vis AS __t_3
        WHERE __t_3."author_id" = __t_2."id"
        ORDER BY __t_3."id" ASC
        LIMIT 101
      ) AS __s_4
      GROUP BY ()
    ) AS __l_6 ON true
    WHERE __t_2."id" = __t_1."author_id"
    LIMIT 1
  ) AS __l_7 ON true
) AS __s_8)) AS data
-- args: [1,"A",1,"B"]

-- field: updatePosts
WITH __mut AS (
  UPDATE "public"."posts"
  SET "published" = $1
  WHERE "public"."posts"."author_id" = $2
  RETURNING "id", "author_id", "title"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "author_id", "title" FROM "public"."posts" WHERE NOT EXISTS (SELECT 1 FROM __mut AS __m_9 WHERE __m_9."id" = "public"."posts"."id")
  UNION ALL SELECT "id", "author_id", "title" FROM __mut
)
SELECT json_build_object('posts', (SELECT coalesce(json_agg(__s_8.data), '[]'::json) FROM (
  SELECT json_build_object('author', __l_7.data) AS data
  FROM __mut AS __t_1
  LEFT JOIN LATERAL (
    SELECT json_build_object('postsByAuthorId', __l_6.data) AS data
    FROM "public"."users" AS __t_2
    LEFT JOIN LATERAL (
      SELECT json_build_object('totalCount', (SELECT count(*) FROM __vis AS __c_5
      WHERE __c_5."author_id" = __t_2."id" AND __c_5."title" LIKE $4)::int) AS data
      FROM (
        SELECT row_number() OVER (ORDER BY __t_3."id" ASC) AS __rn
        FROM __vis AS __t_3
        WHERE __t_3."author_id" = __t_2."id" AND __t_3."title" LIKE $3
        ORDER BY __t_3."id" ASC
        LIMIT 101
      ) AS __s_4
      GROUP BY ()
    ) AS __l_6 ON true
    WHERE __t_2."id" = __t_1."author_id"
    LIMIT 1
  ) AS __l_7 ON true
) AS __s_8)) AS data
-- args: [true,1,"P%","P%"]

-- field: deletePosts
WITH __mut AS (
  DELETE FROM "public"."posts"
  WHERE "public"."posts"."title" LIKE $1
  RETURNING "id", "author_id"
),
__vis AS NOT MATERIALIZED (
  SELECT "id", "author_id" FROM "public"."posts" WHERE NOT EXISTS (SELECT 1 FROM __mut AS __m_9 WHERE __m_9."id" = "public"."posts"."id")
)
SELECT json_build_object('posts', (SELECT coalesce(json_agg(__s_8.data), '[]'::json) FROM (
  SELECT json_build_object('author', __l_7.data) AS data
  FROM __mut AS __t_1
  LEFT JOIN LATERAL (
    SELECT json_build_object('postsByAuthorId', __l_6.data) AS data
    FROM "public"."users" AS __t_2
    LEFT JOIN LATERAL (
      SELECT json_build_object('totalCount', (SELECT count(*) FROM __vis AS __c_5
      WHERE __c_5."author_id" = __t_2."id")::int) AS data
      FROM (
        SELECT row_number() OVER (ORDER BY __t_3."id" ASC) AS __rn
        FROM __vis AS __t_3
        WHERE __t_3."author_id" = __t_2."id"
        ORDER BY __t_3."id" ASC
        LIMIT 101
      ) AS __s_4
      GROUP BY ()
    ) AS __l_6 ON true
    WHERE __t_2."id" = __t_1."author_id"
    LIMIT 1
  ) AS __l_7 ON true
) AS __s_8), 'affectedCount', (SELECT count(*) FROM __mut)) AS data
-- args: ["Draft%"]

-- field: updateCommentById
WITH __mut AS (
  UPDATE "public"."comments"
  SET "body" = $1
  WHERE "public"."comments"."id" = $2
  RETURNING "post_id"
)
SELECT json_build_object('comment', json_build_object('post', __l_3.data)) AS data
FROM __mut AS __t_1
LEFT JOIN LATERAL (
  SELECT json_build_object('title', __t_2."title") AS data
  FROM "public"."posts" AS __t_2
  WHERE __t_2."id" = __t_1."post_id"
  LIMIT 1
) AS __l_3 ON true
-- args: ["Edited",1]

