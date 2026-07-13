-- field: allPosts
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_3.ndata ORDER BY __s_3.__rn DESC) FILTER (WHERE __s_3.__rn <= $3), '[]'::jsonb), 'edges', coalesce(jsonb_agg(jsonb_build_object('cursor', __s_3.__cursor, 'node', __s_3.edata) ORDER BY __s_3.__rn DESC) FILTER (WHERE __s_3.__rn <= $3), '[]'::jsonb), 'pageInfo', jsonb_build_object('hasNextPage', true, 'hasPreviousPage', (count(*) > $3), 'startCursor', (jsonb_agg(__s_3.__cursor ORDER BY __s_3.__rn DESC) FILTER (WHERE __s_3.__rn <= $3))->0, 'endCursor', (jsonb_agg(__s_3.__cursor ORDER BY __s_3.__rn DESC) FILTER (WHERE __s_3.__rn <= $3))->-1)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id", 'title', __t_1."title") AS ndata, jsonb_build_object('id', __t_1."id") AS edata, replace(encode(convert_to(jsonb_build_array('Post'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', '') AS __cursor, row_number() OVER (ORDER BY __t_1."title" DESC, __t_1."id" DESC) AS __rn
  FROM "public"."posts" AS __t_1
  CROSS JOIN (
    SELECT __a."title" AS k1, __a."id" AS k2
    FROM "public"."posts" AS __a
    WHERE __a."id" = $1
  ) AS __anc_2
  WHERE (__t_1."title" < __anc_2.k1 OR (__t_1."title" = __anc_2.k1 AND __t_1."id" < __anc_2.k2))
  ORDER BY __t_1."title" DESC, __t_1."id" DESC
  LIMIT $2
) AS __s_3
GROUP BY ()
-- args: ["9",3,2]

