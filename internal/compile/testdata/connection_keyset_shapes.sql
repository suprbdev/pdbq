-- field: allPosts
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."title" ASC, __t_1."id" ASC) AS __rn
  FROM "public"."posts" AS __t_1
  WHERE (__t_1."title", __t_1."id") > ((SELECT __a."title" FROM "public"."posts" AS __a WHERE __a."id" = $1), $2)
  ORDER BY __t_1."title" ASC, __t_1."id" ASC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["3","3",3,2]

-- field: allPosts
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."title" DESC, __t_1."id" DESC) AS __rn
  FROM "public"."posts" AS __t_1
  WHERE (__t_1."title", __t_1."id") < ((SELECT __a."title" FROM "public"."posts" AS __a WHERE __a."id" = $1), $2)
  ORDER BY __t_1."title" DESC, __t_1."id" DESC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["3","3",3,2]

-- field: allPosts
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn DESC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."author_id" DESC, __t_1."title" DESC, __t_1."id" DESC) AS __rn
  FROM "public"."posts" AS __t_1
  WHERE (__t_1."author_id", __t_1."title", __t_1."id") < ((SELECT __a."author_id" FROM "public"."posts" AS __a WHERE __a."id" = $1), (SELECT __a."title" FROM "public"."posts" AS __a WHERE __a."id" = $1), $2)
  ORDER BY __t_1."author_id" DESC, __t_1."title" DESC, __t_1."id" DESC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["3","3",3,2]

-- field: allPosts
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."title" DESC, __t_1."id" ASC) AS __rn
  FROM "public"."posts" AS __t_1
  WHERE __t_1."title" <= (SELECT __a."title" FROM "public"."posts" AS __a WHERE __a."id" = $1) AND (__t_1."title" < (SELECT __a."title" FROM "public"."posts" AS __a WHERE __a."id" = $1) OR (__t_1."title" = (SELECT __a."title" FROM "public"."posts" AS __a WHERE __a."id" = $1) AND __t_1."id" > $2))
  ORDER BY __t_1."title" DESC, __t_1."id" ASC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["3","3",3,2]

-- field: allPosts
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn DESC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."title" ASC, __t_1."id" DESC) AS __rn
  FROM "public"."posts" AS __t_1
  WHERE __t_1."title" >= (SELECT __a."title" FROM "public"."posts" AS __a WHERE __a."id" = $1) AND (__t_1."title" > (SELECT __a."title" FROM "public"."posts" AS __a WHERE __a."id" = $1) OR (__t_1."title" = (SELECT __a."title" FROM "public"."posts" AS __a WHERE __a."id" = $1) AND __t_1."id" < $2))
  ORDER BY __t_1."title" ASC, __t_1."id" DESC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["3","3",3,2]

-- field: allUsers
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."email" ASC, __t_1."mood" DESC NULLS LAST, __t_1."id" ASC) AS __rn
  FROM "public"."users" AS __t_1
  WHERE __t_1."email" >= (SELECT __a."email" FROM "public"."users" AS __a WHERE __a."id" = $1) AND (__t_1."email" > (SELECT __a."email" FROM "public"."users" AS __a WHERE __a."id" = $1) OR (__t_1."email" = (SELECT __a."email" FROM "public"."users" AS __a WHERE __a."id" = $1) AND ((__t_1."mood" < (SELECT __a."mood" FROM "public"."users" AS __a WHERE __a."id" = $1) OR (__t_1."mood" IS NULL AND (SELECT __a."mood" FROM "public"."users" AS __a WHERE __a."id" = $1) IS NOT NULL)) OR (__t_1."mood" IS NOT DISTINCT FROM (SELECT __a."mood" FROM "public"."users" AS __a WHERE __a."id" = $1) AND __t_1."id" > $2))))
  ORDER BY __t_1."email" ASC, __t_1."mood" DESC NULLS LAST, __t_1."id" ASC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["1","1",3,2]

-- field: allUsers
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."mood" ASC, __t_1."email" ASC, __t_1."id" ASC) AS __rn
  FROM "public"."users" AS __t_1
  WHERE ((__t_1."mood" > (SELECT __a."mood" FROM "public"."users" AS __a WHERE __a."id" = $1) OR (__t_1."mood" IS NULL AND (SELECT __a."mood" FROM "public"."users" AS __a WHERE __a."id" = $1) IS NOT NULL)) OR (__t_1."mood" IS NOT DISTINCT FROM (SELECT __a."mood" FROM "public"."users" AS __a WHERE __a."id" = $1) AND (__t_1."email" > (SELECT __a."email" FROM "public"."users" AS __a WHERE __a."id" = $1) OR (__t_1."email" = (SELECT __a."email" FROM "public"."users" AS __a WHERE __a."id" = $1) AND __t_1."id" > $2))))
  ORDER BY __t_1."mood" ASC, __t_1."email" ASC, __t_1."id" ASC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["1","1",3,2]

