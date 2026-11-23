-- field: createComment
WITH __p_2 AS (
  INSERT INTO "public"."users" ("email") VALUES ($3) RETURNING "id", "email"
),
__p_1 AS (
  INSERT INTO "public"."posts" ("title", "author_id") SELECT $2, __p_2."id" FROM __p_2 RETURNING "id", "author_id", "title"
),
__mut AS (
  INSERT INTO "public"."comments" ("body", "post_id") SELECT $1, __p_1."id" FROM __p_1 RETURNING "id", "post_id", "parent_id", "body"
),
__vis_3 AS NOT MATERIALIZED (
  SELECT "id", "email" FROM "public"."users"
  UNION ALL SELECT "id", "email" FROM __p_2
),
__vis_4 AS NOT MATERIALIZED (
  SELECT "id", "author_id", "title" FROM "public"."posts"
  UNION ALL SELECT "id", "author_id", "title" FROM __p_1
),
__vis_5 AS NOT MATERIALIZED (
  SELECT "id", "post_id", "parent_id", "body" FROM "public"."comments"
  UNION ALL SELECT "id", "post_id", "parent_id", "body" FROM __mut
)
SELECT json_build_object('comment', __row.data) AS data
FROM (
  SELECT json_build_object('body', __t_1."body", 'post', __l_13.data) AS data
  FROM __mut AS __t_1
  LEFT JOIN LATERAL (
    SELECT json_build_object('title', __t_2."title", 'author', __l_8.data, 'commentsByPostId', __l_12.data) AS data
    FROM __vis_4 AS __t_2
    LEFT JOIN LATERAL (
      SELECT json_build_object('email', __t_3."email", 'postsByAuthorId', __l_7.data) AS data
      FROM __vis_3 AS __t_3
      LEFT JOIN LATERAL (
        SELECT json_build_object('totalCount', (SELECT count(*) FROM __vis_4 AS __c_6
        WHERE __c_6."author_id" = __t_3."id")::int, 'nodes', coalesce(json_agg(__s_5.ndata ORDER BY __s_5.__rn ASC) FILTER (WHERE __s_5.__rn <= $5), '[]'::json)) AS data
        FROM (
          SELECT json_build_object('title', __t_4."title") AS ndata, row_number() OVER (ORDER BY __t_4."id" ASC) AS __rn
          FROM __vis_4 AS __t_4
          WHERE __t_4."author_id" = __t_3."id"
          ORDER BY __t_4."id" ASC
          LIMIT $4
        ) AS __s_5
        GROUP BY ()
      ) AS __l_7 ON true
      WHERE __t_3."id" = __t_2."author_id"
      LIMIT 1
    ) AS __l_8 ON true
    LEFT JOIN LATERAL (
      SELECT json_build_object('totalCount', (SELECT count(*) FROM __vis_5 AS __c_11
      WHERE __c_11."post_id" = __t_2."id" AND __c_11."parent_id" IS NULL)::int, 'nodes', coalesce(json_agg(__s_10.ndata ORDER BY __s_10.__rn ASC) FILTER (WHERE __s_10.__rn <= $7), '[]'::json)) AS data
      FROM (
        SELECT json_build_object('body', __t_9."body") AS ndata, row_number() OVER (ORDER BY __t_9."id" ASC) AS __rn
        FROM __vis_5 AS __t_9
        WHERE __t_9."post_id" = __t_2."id" AND __t_9."parent_id" IS NULL
        ORDER BY __t_9."id" ASC
        LIMIT $6
      ) AS __s_10
      GROUP BY ()
    ) AS __l_12 ON true
    WHERE __t_2."id" = __t_1."post_id"
    LIMIT 1
  ) AS __l_13 ON true
) AS __row
-- args: ["First!","Hello","new@example.com",101,100,101,100]

-- field: createUser
WITH __mut AS (
  INSERT INTO "public"."users" ("email") VALUES ($1) RETURNING "id", "email"
),
__c_1 AS (
  INSERT INTO "public"."posts" ("title", "author_id") SELECT $2, __mut."id" FROM __mut RETURNING "id", "author_id", "title"
),
__c_2 AS (
  INSERT INTO "public"."comments" ("body", "post_id") SELECT $3, __c_1."id" FROM __c_1 RETURNING "id", "post_id", "parent_id", "body"
),
__c_3 AS (
  INSERT INTO "public"."comments" ("body", "post_id") SELECT $4, __c_1."id" FROM __c_1 RETURNING "id", "post_id", "parent_id", "body"
),
__c_4 AS (
  INSERT INTO "public"."posts" ("title", "author_id") SELECT $5, __mut."id" FROM __mut RETURNING "id", "author_id", "title"
),
__vis_5 AS NOT MATERIALIZED (
  SELECT "id", "email" FROM "public"."users"
  UNION ALL SELECT "id", "email" FROM __mut
),
__vis_6 AS NOT MATERIALIZED (
  SELECT "id", "author_id", "title" FROM "public"."posts"
  UNION ALL SELECT "id", "author_id", "title" FROM __c_1
  UNION ALL SELECT "id", "author_id", "title" FROM __c_4
),
__vis_7 AS NOT MATERIALIZED (
  SELECT "id", "post_id", "parent_id", "body" FROM "public"."comments"
  UNION ALL SELECT "id", "post_id", "parent_id", "body" FROM __c_2
  UNION ALL SELECT "id", "post_id", "parent_id", "body" FROM __c_3
)
SELECT json_build_object('user', __row.data) AS data
FROM (
  SELECT json_build_object('email', __t_1."email", 'postsByAuthorId', __l_13.data) AS data
  FROM __mut AS __t_1
  LEFT JOIN LATERAL (
    SELECT json_build_object('totalCount', (SELECT count(*) FROM __vis_6 AS __c_11
    WHERE __c_11."author_id" = __t_1."id" AND EXISTS (SELECT 1 FROM __vis_7 AS __r_12 WHERE __r_12."post_id" = __c_11."id" AND (__r_12."parent_id" IS NULL)))::int, 'nodes', coalesce(json_agg(__s_10.ndata ORDER BY __s_10.__rn ASC) FILTER (WHERE __s_10.__rn <= $9), '[]'::json)) AS data
    FROM (
      SELECT json_build_object('title', __t_2."title", 'author', __l_4.data, 'commentsByPostId', __l_8.data) AS ndata, row_number() OVER (ORDER BY __t_2."id" ASC) AS __rn
      FROM __vis_6 AS __t_2
      LEFT JOIN LATERAL (
        SELECT json_build_object('email', __t_3."email") AS data
        FROM __vis_5 AS __t_3
        WHERE __t_3."id" = __t_2."author_id"
        LIMIT 1
      ) AS __l_4 ON true
      LEFT JOIN LATERAL (
        SELECT json_build_object('totalCount', (SELECT count(*) FROM __vis_7 AS __c_7
        WHERE __c_7."post_id" = __t_2."id")::int, 'nodes', coalesce(json_agg(__s_6.ndata ORDER BY __s_6.__rn ASC) FILTER (WHERE __s_6.__rn <= $7), '[]'::json)) AS data
        FROM (
          SELECT json_build_object('body', __t_5."body") AS ndata, row_number() OVER (ORDER BY __t_5."id" ASC) AS __rn
          FROM __vis_7 AS __t_5
          WHERE __t_5."post_id" = __t_2."id"
          ORDER BY __t_5."id" ASC
          LIMIT $6
        ) AS __s_6
        GROUP BY ()
      ) AS __l_8 ON true
      WHERE __t_2."author_id" = __t_1."id" AND EXISTS (SELECT 1 FROM __vis_7 AS __r_9 WHERE __r_9."post_id" = __t_2."id" AND (__r_9."parent_id" IS NULL))
      ORDER BY __t_2."id" ASC
      LIMIT $8
    ) AS __s_10
    GROUP BY ()
  ) AS __l_13 ON true
) AS __row
-- args: ["kids@example.com","One","a","b","Two",101,100,101,100]

-- field: createPost
WITH __p_1 AS (
  SELECT "id" FROM "public"."users" WHERE "public"."users"."id" = $2
),
__mut AS (
  INSERT INTO "public"."posts" ("title", "author_id") SELECT $1, __p_1."id" FROM __p_1 RETURNING "id", "author_id", "title"
),
__c_2 AS (
  INSERT INTO "public"."comments" ("body", "post_id") SELECT $3, __mut."id" FROM __mut RETURNING "id", "post_id", "body"
),
__vis_3 AS NOT MATERIALIZED (
  SELECT "id", "author_id" FROM "public"."posts"
  UNION ALL SELECT "id", "author_id" FROM __mut
),
__vis_4 AS NOT MATERIALIZED (
  SELECT "id", "post_id", "body" FROM "public"."comments"
  UNION ALL SELECT "id", "post_id", "body" FROM __c_2
),
__guard AS MATERIALIZED (
  SELECT (SELECT CASE WHEN count(*) > 0 THEN true ELSE concat('nested-mutations: author.connectById matched ', count(*), ' rows')::boolean END FROM __p_1) AS ok
)
SELECT __guarded.data
FROM __guard
LEFT JOIN LATERAL (
  SELECT json_build_object('post', __row.data) AS data
  FROM (
    SELECT json_build_object('title', __t_1."title", 'author', __l_7.data, 'commentsByPostId', __l_10.data) AS data
    FROM __mut AS __t_1
    LEFT JOIN LATERAL (
      SELECT json_build_object('email', __t_2."email", 'postsByAuthorId', __l_6.data) AS data
      FROM "public"."users" AS __t_2
      LEFT JOIN LATERAL (
        SELECT json_build_object('totalCount', (SELECT count(*) FROM __vis_3 AS __c_5
        WHERE __c_5."author_id" = __t_2."id")::int) AS data
        FROM (
          SELECT row_number() OVER (ORDER BY __t_3."id" ASC) AS __rn
          FROM __vis_3 AS __t_3
          WHERE __t_3."author_id" = __t_2."id"
          ORDER BY __t_3."id" ASC
          LIMIT $4
        ) AS __s_4
        GROUP BY ()
      ) AS __l_6 ON true
      WHERE __t_2."id" = __t_1."author_id"
      LIMIT 1
    ) AS __l_7 ON true
    LEFT JOIN LATERAL (
      SELECT json_build_object('nodes', coalesce(json_agg(__s_9.ndata ORDER BY __s_9.__rn ASC) FILTER (WHERE __s_9.__rn <= $6), '[]'::json)) AS data
      FROM (
        SELECT json_build_object('body', __t_8."body") AS ndata, row_number() OVER (ORDER BY __t_8."id" ASC) AS __rn
        FROM __vis_4 AS __t_8
        WHERE __t_8."post_id" = __t_1."id"
        ORDER BY __t_8."id" ASC
        LIMIT $5
      ) AS __s_9
      GROUP BY ()
    ) AS __l_10 ON true
  ) AS __row
) AS __guarded ON true
WHERE __guard.ok
-- args: ["Connected",1,"c",101,101,100]

